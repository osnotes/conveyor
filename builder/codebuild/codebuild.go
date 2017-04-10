package codebuild

import (
	"fmt"
	"log"
	"text/template"
	"bytes"
	"strings"
	"io"
	"os"
	"errors"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/client"
	"github.com/aws/aws-sdk-go/service/codebuild"
	"github.com/remind101/conveyor/builder"
	"golang.org/x/net/context"
	"github.com/remind101/conveyor/logs/cloudwatch"
)

const (
	// Default Image for codebuild
	DefaultCodebuildImage = "aws/codebuild/docker:1.12.1"

	// Default AWS resource used by codebuild
	DefaultCodebuildComputeType = "BUILD_GENERAL1_SMALL"

	// Number of times to retry fetching the logs
	RetryCall = 5

)

// Builder is a builder.Builder implementation that runs the build in a docker
// container.
type Builder struct {
	codebuild *codebuild.CodeBuild

	// Required field, arn of the instance-role required by codebuild
	ServiceRole string

	// The Image used by codebuild to build images. Defaults to 
	// DefaultCodebuildImage
	Image string

	// The computing instances AWS CodeBuild will use. Defaults to
	// DefaultCodebuildComputeType
	ComputeType string

	// Credentials for Dockerhub
	DockerUsername string
	DockerPassword string
}

type BuildSpecInput struct {
	// Extend all the values already given in builder
	*Builder

	// The repository which is being built
	Repository string

	// The commit sha at which to do the build
	Sha string

	// The branch at which the build is happening
	Branch string
}

type LogInfo struct {
	// Name of the cloudwatch group
	GroupName string

	// Name of cloudwatch stream
	StreamName string
}

// NewBuilder returns a new Builder backed by the codebuild client.
func NewBuilder(config client.ConfigProvider) *Builder {
	return &Builder{
		codebuild: codebuild.New(config),
	}
}

// NewBuilderFromEnv returns a new Builder with a codebuild client
// configured from the standard Docker environment variables.
func NewBuilderFromEnv() (*Builder, error) {	

	serviceRole := os.Getenv("CODEBUILD_SERVICE_ROLE")

	if serviceRole == "" {
		return nil, errors.New("CODEBUILD_SERVICE_ROLE must be set when using codebuild builder")
	}

	image := os.Getenv("CODEBUILD_IMAGE")

	if image == "" {
		image = DefaultCodebuildImage
	}

	computeType := os.Getenv("CODEBUILD_COMPUTE_TYPE")

	if computeType == "" {
		computeType = DefaultCodebuildComputeType
	}

	dockerUsername := os.Getenv("DOCKER_USERNAME")
	dockerPassword := os.Getenv("DOCKER_PASSWORD")

	if dockerUsername == "" || dockerPassword == "" {
		return nil, errors.New("DOCKER_USERNAME and DOCKER_PASSWORD env vars must be set when using codebuild builder")
	}

	sess := session.Must(session.NewSession())

	return &Builder{
		codebuild: codebuild.New(sess),
		ServiceRole: serviceRole,
		Image: image,
		ComputeType: computeType,
		DockerUsername: dockerUsername,
		DockerPassword: dockerPassword,
	}, nil
}


// Build executes the docker image.
func (b *Builder) Build(ctx context.Context, w io.Writer, opts builder.BuildOptions) (image string, err error) {
	image = fmt.Sprintf("%s:%s", opts.Repository, opts.Sha)
	err = b.build(ctx, w, opts)
	return
}

// Build executes the codebuild image.
func (b *Builder) build(ctx context.Context, w io.Writer, opts builder.BuildOptions) error {
	
	projectName := strings.Join([]string{
		"conveyor",
		strings.Replace(opts.Repository, "/", "_", -1),
	}, "-")

	startBuild, err := b.startBuild(opts, projectName)

	if err != nil {

		awsErr, ok := err.(awserr.Error)

    	if ok && awsErr.Code() == "ResourceNotFoundException" {

	        _, err = b.createProject(opts, projectName)

			if err != nil {
				return err
			} 

			startBuild, err = b.startBuild(opts, projectName)
			
			if err != nil {
				log.Fatal(err)
				return err
			}
	        
	    } else {
	    	log.Fatal(err)
	    	return err
	   	}

    }

    logInfo, err := b.getLogInfo(*startBuild.Build.Id)

    if err != nil {
    	log.Fatal(err)
    	return err
    }


    sess := session.Must(session.NewSession())

    r, err := cloudwatch.NewLogger(sess, logInfo.GroupName).Open(logInfo.StreamName)
   	
   	if err != nil {
   		log.Fatal(err)
   		return err
   	}

    _, err = io.Copy(w, r)

    if err != nil {
    	log.Fatal(err)
    	return err
    }

    return nil
}

func (b *Builder) getLogInfo(buildId string) (logInfo *LogInfo, err error) {

	params := &codebuild.BatchGetBuildsInput{
	    Ids: []*string{ 
	        aws.String(buildId),
	    },
	}

	buildInfoResp, err := b.codebuild.BatchGetBuilds(params)

	if err != nil {
		return nil, err
	}

	for i := 0; i <= RetryCall; i++ {

		if i == RetryCall {
			return nil, errors.New("Log stream name could not be fetched, retry limit hit")
		}
    	
    	if buildInfoResp.Builds[0].Logs == nil {

    		time.Sleep(time.Second * 1)
    		buildInfoResp, err = b.codebuild.BatchGetBuilds(params)

    		if err != nil {
    			return nil, err
    		}


    	} else {
    		break;
    	}

    }

    return &LogInfo{
    	GroupName: *buildInfoResp.Builds[0].Logs.GroupName,
    	StreamName: *buildInfoResp.Builds[0].Logs.StreamName,
    }, nil

}

func (b *Builder) createProject(opts builder.BuildOptions, projectName string) (resp *codebuild.CreateProjectOutput, err error) {

	log.Printf("Creating a new codebuild project: %s", projectName)

	githubSource := fmt.Sprintf("https://github.com/%s", opts.Repository)
	
	buildParams := &codebuild.CreateProjectInput{
	    Artifacts: &codebuild.ProjectArtifacts{
	        Type:          aws.String("NO_ARTIFACTS"),
	    },
	    Environment: &codebuild.ProjectEnvironment{
	        ComputeType: aws.String(b.ComputeType),
	        Image:       aws.String(b.Image),
	        Type:        aws.String("LINUX_CONTAINER"),
	    },
	    Name: aws.String(projectName),
	    Source: &codebuild.ProjectSource{ 
	        Type: aws.String("GITHUB"), 
	        Auth: &codebuild.SourceAuth{
	            Type:     aws.String("OAUTH"),
	        },
	        Location:  aws.String(githubSource),
	    },
	    ServiceRole:   aws.String(b.ServiceRole),
	}

	resp, err = b.codebuild.CreateProject(buildParams)

	if err != nil {

		return nil, err
	}

	return resp, nil
}

func (b *Builder) startBuild(opts builder.BuildOptions, projectName string) (resp *codebuild.StartBuildOutput, err error) {
	
	buildspec, err := b.generateBuildspec(opts)

	if err != nil {
		return
	}

	params := &codebuild.StartBuildInput{
		ProjectName:   aws.String(projectName),
		SourceVersion: aws.String(opts.Sha),
		BuildspecOverride: aws.String(buildspec),
	}

	resp, err = b.codebuild.StartBuild(params)
	return

}


func (b *Builder) generateBuildspec(opts builder.BuildOptions) (buildspec string, err error) {
	
	if b.Image != DefaultCodebuildImage {
		err = errors.New("Please include a custom buildspec when using a different build Image")
		return
	}

	params := BuildSpecInput{b, opts.Repository, opts.Sha, opts.Branch}
	
	specTemplate := `version: 0.1

environment_variables:
  plaintext:
    DOCKER_USERNAME: {{.DockerUsername}}
    DOCKER_PASSWORD: {{.DockerPassword}}

phases:
  pre_build:
    commands:
      - docker login -u ${DOCKER_USERNAME} -p ${DOCKER_PASSWORD}
      - echo "Logged into Docker"
      - docker pull "{{.Repository}}:${{.Branch}}" || docker pull "{{.Repository}}:master" || true
      - echo "Pulled Image"
  build:
    commands:
      - docker build -t "{{.Repository}}" .
      - echo "Built Image with tag {{.Repository}}"
      - docker tag "{{.Repository}}" "{{.Repository}}:{{.Branch}}"
      - docker tag "{{.Repository}}" "{{.Repository}}:{{.Sha}}"
  post_build:
    commands:
      - docker push "{{.Repository}}:{{.Sha}}"
      - docker push "{{.Repository}}:{{.Branch}}"
      - docker push "{{.Repository}}:latest"
      - echo "Done pushing to docker registry"
`

	tmpl, err := template.New("buildspec").Parse(specTemplate)

	if err != nil {
		return
	}

	buf := new(bytes.Buffer)
	err = tmpl.Execute(buf, params)

	if err != nil {
		return
	}

	buildspec = buf.String()
	return
	
}
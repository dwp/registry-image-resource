package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ecr"
	resource "github.com/concourse/registry-image-resource"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/sirupsen/logrus"
)

type CheckRequest struct {
	Source  resource.Source   `json:"source"`
	Version *resource.Version `json:"version"`
}

type CheckResponse []resource.Version

func main() {
	logrus.SetOutput(os.Stderr)
	logrus.SetFormatter(&logrus.TextFormatter{
		ForceColors: true,
	})

	var req CheckRequest
	decoder := json.NewDecoder(os.Stdin)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&req)
	if err != nil {
		logrus.Errorf("invalid payload: %s", err)
		os.Exit(1)
		return
	}

	n, err := name.ParseReference(req.Source.Name(), name.WeakValidation)
	if err != nil {
		logrus.Errorf("could not resolve repository/tag reference: %s", err)
		os.Exit(1)
		return
	}

	if req.Source.Ecr {
		mySession := session.Must(session.NewSession())
		client := ecr.New(mySession, aws.NewConfig().WithRegion("eu-west-2"))
		input := &ecr.GetAuthorizationTokenInput{}
		result, err := client.GetAuthorizationToken(input)
		if err != nil {
			if aerr, ok := err.(awserr.Error); ok {
				switch aerr.Code() {
				case ecr.ErrCodeServerException:
					fmt.Println(ecr.ErrCodeServerException, aerr.Error())
				case ecr.ErrCodeInvalidParameterException:
					fmt.Println(ecr.ErrCodeInvalidParameterException, aerr.Error())
				default:
					fmt.Println(aerr.Error())
				}
			} else {
				// Print the error, cast err to awserr.Error to get the Code and
				// Message from an error.
				fmt.Println(err.Error())
			}
			return
		}

		// Update username, password and repository
		req.Source.Username = "AWS"
		req.Source.Password = *result.AuthorizationData[0].AuthorizationToken
		req.Source.Repository = strings.Join([]string{strings.Replace(*result.AuthorizationData[0].ProxyEndpoint, "https://", "", -1), req.Source.Repository}, "/")
	}

	auth := &authn.Basic{
		Username: req.Source.Username,
		Password: req.Source.Password,
	}

	imageOpts := []remote.ImageOption{
		remote.WithTransport(resource.RetryTransport),
	}

	if auth.Username != "" && auth.Password != "" {
		imageOpts = append(imageOpts, remote.WithAuth(auth))
	}

	image, err := remote.Image(n, imageOpts...)
	if err != nil {
		logrus.Errorf("failed to get remote image: %s", err)
		os.Exit(1)
		return
	}

	digest, err := image.Digest()
	if err != nil {
		logrus.Errorf("failed get image digest: %s", err)
		os.Exit(1)
		return
	}

	response := CheckResponse{}
	if req.Version != nil && req.Version.Digest != digest.String() {
		digestRef, err := name.ParseReference(req.Source.Repository+"@"+req.Version.Digest, name.WeakValidation)
		if err != nil {
			logrus.Errorf("could not resolve repository/digest reference: %s", err)
			os.Exit(1)
			return
		}

		var digestImage v1.Image
		if auth.Username != "" && auth.Password != "" {
			digestImage, err = remote.Image(digestRef, remote.WithAuth(auth))
		} else {
			digestImage, err = remote.Image(digestRef)
		}
		if err != nil {
			logrus.Errorf("failed to get remote image: %s", err)
			os.Exit(1)
			return
		}

		var missingDigest bool
		_, err = digestImage.Digest()
		if err != nil {
			if rErr, ok := err.(*remote.Error); ok {
				for _, e := range rErr.Errors {
					if e.Code == remote.ManifestUnknownErrorCode {
						missingDigest = true
						break
					}
				}
			}

			if !missingDigest {
				logrus.Errorf("failed to get cursor image digest: %s", err)
				os.Exit(1)
				return
			}
		}

		if !missingDigest {
			response = append(response, *req.Version)
		}
	}

	response = append(response, resource.Version{
		Digest: digest.String(),
	})

	json.NewEncoder(os.Stdout).Encode(response)
}

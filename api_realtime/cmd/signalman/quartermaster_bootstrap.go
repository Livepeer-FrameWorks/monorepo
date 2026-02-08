package main

import (
	"context"
	"fmt"

	pb "frameworks/pkg/proto"
)

type quartermasterBootstrapper interface {
	BootstrapService(ctx context.Context, req *pb.BootstrapServiceRequest) (*pb.BootstrapServiceResponse, error)
}

func bootstrapSignalmanService(ctx context.Context, bootstrapper quartermasterBootstrapper, req *pb.BootstrapServiceRequest) error {
	if bootstrapper == nil {
		return fmt.Errorf("quartermaster bootstrapper is nil")
	}

	_, err := bootstrapper.BootstrapService(ctx, req)
	if err != nil {
		return err
	}

	return nil
}

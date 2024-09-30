### day3

#### protobuf client and server

- proto 文件

  ```protobuf
  syntax = "proto3";
  package log.v1;
  option go_package = "github.com/saveole/api/log_v1";
  
  message Record {
      bytes value = 1;
      uint64 offset = 2;
  }
  
  service Log {
      rpc Produce(ProduceRequest) returns (ProduceResponse){}
      rpc Consume(ConsumeRequest) returns (ConsumeResponse){}
      rpc ProduceStream(stream ProduceRequest) returns (stream ProduceResponse){}
      rpc ConsumeStream(ConsumeRequest) returns (stream ConsumeResponse){}
  }
  
  message ProduceRequest {
      Record record = 1;
  }
  
  message ProduceResponse {
      uint64 offset = 1;
  }
  
  message ConsumeRequest {
      uint64 offset = 1;
  }
  
  message ConsumeResponse {
      Record record = 2;
  }
  ```

- 生成 go 文件：

  ```shell
  protoc api/v1/*.proto \
          --go_out=. \
          --go-grpc_out=. \
          --go_opt=paths=source_relative \
          --go-grpc_opt=paths=source_relative \
          --proto_path=.
  ```

  

#### server.go

- 构建/注册 grpc server，实现 proto 文件中定义的 service 中的方法。

  ```go
  package server
  
  import (
  	"context"
  
  	api "github.com/saveole/proglog/api/v1"
  	log_v1 "github.com/saveole/proglog/api/v1"
  	"google.golang.org/grpc"
  )
  
  type Config struct {
  	CommitLog CommitLog
  }
  
  type CommitLog interface {
  	Append(*log_v1.Record) (uint64, error)
  	Read(uint64) (*log_v1.Record, error)
  }
  
  type grpcServer struct {
  	api.UnimplementedLogServer
  	*Config
  }
  
  func NewGRPCServer(config *Config) (*grpc.Server, error) {
  	gsrv := grpc.NewServer()
  	srv, err := newgrpcServer(config)
  	if err != nil {
  		return nil, err
  	}
  	api.RegisterLogServer(gsrv, srv)
  	return gsrv, nil
  }
  
  func newgrpcServer(config *Config) (srv *grpcServer, err error) {
  	srv = &grpcServer{
  		Config: config,
  	}
  	return srv, nil
  }
  
  func (s *grpcServer) Produce(ctx context.Context, req *api.ProduceRequest) (
  	*api.ProduceResponse, error) {
  	offset, err := s.CommitLog.Append(req.Record)
  	if err != nil {
  		return nil, err
  	}
  	return &api.ProduceResponse{Offset: uint64(offset)}, nil
  }
  
  func (s *grpcServer) Consume(ctx context.Context, req *api.ConsumeRequest) (
  	*api.ConsumeResponse, error) {
  	record, err := s.CommitLog.Read(req.Offset)
  	if err != nil {
  		return nil, err
  	}
  	return &api.ConsumeResponse{Record: record}, nil
  }
  
  func (s *grpcServer) ProduceStream(
  	stream api.Log_ProduceStreamServer,
  ) error {
  	for {
  		req, err := stream.Recv()
  		if err != nil {
  			return err
  		}
  		res, err := s.Produce(stream.Context(), req)
  		if err != nil {
  			return err
  		}
  		if err = stream.Send(res); err != nil {
  			return err
  		}
  	}
  }
  
  func (s *grpcServer) ConsumeStream(
  	req *api.ConsumeRequest, stream api.Log_ConsumeStreamServer,
  ) error {
  	for {
  		select {
  		case <-stream.Context().Done():
  			return nil
  		default:
  			res, err := s.Consume(stream.Context(), req)
  			switch err.(type) {
  			case nil:
  			case api.ErrOffsetOutOfRange:
  				continue
  			default:
  				return err
  			}
  			if err = stream.Send(res); err != nil {
  				return nil
  			}
  			req.Offset++
  		}
  	}
  }
  
  ```

  


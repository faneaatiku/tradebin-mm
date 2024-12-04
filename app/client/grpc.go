package client

import (
	authv1beta1 "cosmossdk.io/api/cosmos/auth/v1beta1"
	bankv1beta1 "cosmossdk.io/api/cosmos/bank/v1beta1"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	tradebinTypes "github.com/bze-alphateam/bze/x/tradebin/types"
	sdkTx "github.com/cosmos/cosmos-sdk/types/tx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
)

const (
	lockName = "grpc:client:connection"
)

type ConnectionLocker interface {
	Lock(key string)
	Unlock(key string)
}

type GrpcClient struct {
	host   string
	useTLS bool
	locker ConnectionLocker
	conn   *grpc.ClientConn
}

func NewGrpcClient(host string, useTls bool, locker ConnectionLocker) (*GrpcClient, error) {
	if host == "" {
		return nil, fmt.Errorf("grpc host is required")
	}

	if locker == nil {
		return nil, fmt.Errorf("grpc client requires locker")
	}

	return &GrpcClient{
		host:   host,
		locker: locker,
		useTLS: useTls,
	}, nil
}

// LoadTLSCredentials loads TLS credentials with ALPN support for HTTP/2
func (c *GrpcClient) loadTLSCredentials() (credentials.TransportCredentials, error) {
	// Load system CA certificates or specific certs
	certPool, err := x509.SystemCertPool()
	if err != nil {
		return nil, err
	}

	// Create the TLS config, explicitly specifying HTTP/2 via ALPN
	tlsConfig := &tls.Config{
		RootCAs:            certPool,       // Use system CAs
		NextProtos:         []string{"h2"}, // HTTP/2 (gRPC requires this)
		InsecureSkipVerify: false,          // Verify server certificate
	}

	// Return the transport credentials for gRPC to use
	return credentials.NewTLS(tlsConfig), nil
}

func (c *GrpcClient) getConnection() (*grpc.ClientConn, error) {
	//make it thread safe
	c.locker.Lock(lockName)
	defer c.locker.Unlock(lockName)
	if c.conn != nil && c.conn.GetState() != connectivity.Shutdown {
		return c.conn, nil
	}

	var dialOptions []grpc.DialOption
	if c.useTLS {
		cred, err := c.loadTLSCredentials()
		if err != nil {
			return nil, err
		}
		
		dialOptions = append(dialOptions, grpc.WithTransportCredentials(cred))
	} else {
		dialOptions = append(dialOptions, grpc.WithInsecure())
	}

	grpcConn, err := grpc.Dial(
		c.host,
		dialOptions...,
	)

	if err != nil {
		return nil, err
	}

	c.conn = grpcConn

	return grpcConn, nil
}

func (c *GrpcClient) GetTradebinQueryClient() (tradebinTypes.QueryClient, error) {
	grpcConn, err := c.getConnection()
	if err != nil {
		return nil, err
	}

	queryClient := tradebinTypes.NewQueryClient(grpcConn)

	return queryClient, nil
}

func (c *GrpcClient) GetBankQueryClient() (bankv1beta1.QueryClient, error) {
	grpcConn, err := c.getConnection()
	if err != nil {
		return nil, err
	}

	queryClient := bankv1beta1.NewQueryClient(grpcConn)

	return queryClient, nil
}

func (c *GrpcClient) GetAuthQueryClient() (authv1beta1.QueryClient, error) {
	grpcConn, err := c.getConnection()
	if err != nil {
		return nil, err
	}

	queryClient := authv1beta1.NewQueryClient(grpcConn)

	return queryClient, nil
}

func (c *GrpcClient) CloseConnection() {
	if c.conn != nil {
		_ = c.conn.Close()
	}
}

func (c *GrpcClient) GetServiceClient() (sdkTx.ServiceClient, error) {
	conn, err := c.getConnection()
	if err != nil {
		return nil, err
	}

	sc := sdkTx.NewServiceClient(conn)

	return sc, nil
}

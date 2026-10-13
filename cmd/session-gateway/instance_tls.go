package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"os"
)

func instanceControlOptions(enabled bool) ([]grpc.ServerOption, error) {
	if !enabled && os.Getenv("INSTANCE_CONTROL_CA_FILE") == "" && os.Getenv("INSTANCE_CONTROL_CERT_FILE") == "" && os.Getenv("INSTANCE_CONTROL_KEY_FILE") == "" {
		return nil, nil
	}
	ca, e := os.ReadFile(os.Getenv("INSTANCE_CONTROL_CA_FILE"))
	if e != nil {
		return nil, fmt.Errorf("managed instance control CA required")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("managed instance control CA invalid")
	}
	cert, e := tls.LoadX509KeyPair(os.Getenv("INSTANCE_CONTROL_CERT_FILE"), os.Getenv("INSTANCE_CONTROL_KEY_FILE"))
	if e != nil {
		return nil, fmt.Errorf("managed instance control identity required")
	}
	return []grpc.ServerOption{grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert, Certificates: []tls.Certificate{cert}}))}, nil
}

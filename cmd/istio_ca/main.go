// Copyright 2017 Istio Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"time"

	"istio.io/auth/certmanager"
	"istio.io/auth/cmd/istio_ca/version"
	"istio.io/auth/controller"

	"github.com/golang/glog"
	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	// The default issuer organization for self-signed CA certificate.
	selfSignedCAOrgDefault = "k8s.cluster.local"

	// The key for the environment variable that specifies the namespace.
	namespaceKey = "NAMESPACE"
)

type cliOptions struct {
	certChainFile   string
	signingCertFile string
	signingKeyFile  string
	rootCertFile    string

	namespace      string
	kubeConfigFile string

	selfSignedCA    bool
	selfSignedCAOrg string

	caCertTTL time.Duration
	certTTL   time.Duration
}

var (
	opts cliOptions

	rootCmd = &cobra.Command{
		Run: func(cmd *cobra.Command, args []string) {
			runCA()
		},
	}
)

func init() {
	flags := rootCmd.Flags()

	flags.StringVar(&opts.certChainFile, "cert-chain", "", "Speicifies path to the certificate chain file")
	flags.StringVar(&opts.signingCertFile, "signing-cert", "", "Specifies path to the CA signing certificate file")
	flags.StringVar(&opts.signingKeyFile, "signing-key", "", "Specifies path to the CA signing key file")
	flags.StringVar(&opts.rootCertFile, "root-cert", "", "Specifies path to the root certificate file")

	flags.StringVar(&opts.namespace, "namespace", "",
		"Select a namespace for the CA to listen to. If unspecified, Istio CA tries to use the ${"+namespaceKey+"} "+
			"environment variable. If neither is set, Istio CA listens to all namespaces.")
	flags.StringVar(&opts.kubeConfigFile, "kube-config", "",
		"Specifies path to kubeconfig file. This must be specified when not running inside a Kubernetes pod.")

	flags.BoolVar(&opts.selfSignedCA, "self-signed-ca", false,
		"Indicates whether to use auto-generated self-signed CA certificate. "+
			"When set to true, the '--signing-cert' and '--signing-key' options are ignored.")
	flags.StringVar(&opts.selfSignedCAOrg, "self-signed-ca-org", "k8s.cluster.local",
		fmt.Sprintf("The issuer organization used in self-signed CA certificate (default to %s)",
			selfSignedCAOrgDefault))

	flags.DurationVar(&opts.caCertTTL, "ca-cert-ttl", 240*time.Hour,
		"The TTL of self-signed CA root certificate (default to 10 days)")
	flags.DurationVar(&opts.certTTL, "cert-ttl", time.Hour, "The TTL of issued certificates (default to 1 hour)")

	rootCmd.AddCommand(version.Command)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		glog.Error(err)
		os.Exit(-1)
	}
}

func runCA() {
	if opts.namespace == "" {
		// When -namespace is not set, try to read the namespace from environment variable.
		if value, exists := os.LookupEnv(namespaceKey); exists {
			opts.namespace = value
		}
	}

	verifyCommandLineOptions()

	ca := createCA()
	cs := createClientset()
	sc := controller.NewSecretController(ca, cs.CoreV1(), opts.namespace)

	stopCh := make(chan struct{})
	sc.Run(stopCh)

	<-stopCh
	glog.Warning("Istio CA has stopped")
}

func createClientset() *kubernetes.Clientset {
	c := generateConfig()
	cs, err := kubernetes.NewForConfig(c)
	if err != nil {
		glog.Fatalf("Failed to create a clientset (error: %s)", err)
	}
	return cs
}

func createCA() certmanager.CertificateAuthority {
	if opts.selfSignedCA {
		glog.Info("Use self-signed certificate as the CA certificate")

		ca, err := certmanager.NewSelfSignedIstioCA(opts.caCertTTL, opts.certTTL, opts.selfSignedCAOrg)
		if err != nil {
			glog.Fatalf("Failed to create a self-signed Istio CA (error: %v)", err)
		}
		return ca
	}

	caOpts := &certmanager.IstioCAOptions{
		CertChainBytes:   readFile(opts.certChainFile),
		CertTTL:          opts.certTTL,
		SigningCertBytes: readFile(opts.signingCertFile),
		SigningKeyBytes:  readFile(opts.signingKeyFile),
		RootCertBytes:    readFile(opts.rootCertFile),
	}
	ca, err := certmanager.NewIstioCA(caOpts)
	if err != nil {
		glog.Errorf("Failed to create an Istio CA (error %v)", err)
	}
	return ca
}

func generateConfig() *rest.Config {
	if opts.kubeConfigFile != "" {
		c, err := clientcmd.BuildConfigFromFlags("", opts.kubeConfigFile)
		if err != nil {
			glog.Fatalf("Failed to create a config object from file %s, (error %v)", opts.kubeConfigFile, err)
		}
		return c
	}

	// When `kubeConfigFile` is unspecified, use the in-cluster configuration.
	c, err := rest.InClusterConfig()
	if err != nil {
		glog.Fatalf("Failed to create a in-cluster config (error: %s)", err)
	}
	return c
}

func readFile(filename string) []byte {
	bs, err := ioutil.ReadFile(filename)
	if err != nil {
		glog.Fatalf("Failed to read file %s (error: %v)", filename, err)
	}
	return bs
}

func verifyCommandLineOptions() {
	if opts.selfSignedCA {
		return
	}

	if opts.certChainFile == "" {
		glog.Fatalf(
			"No certificate chain has been specified. Either specify a cert chain file via '-cert-chain' option " +
				"or use '-self-signed-ca'")
	}

	if opts.signingCertFile == "" {
		glog.Fatalf(
			"No signing cert has been specified. Either specify a cert file via '-signing-cert' option " +
				"or use '-self-signed-ca'")
	}

	if opts.signingKeyFile == "" {
		glog.Fatalf(
			"No signing key has been specified. Either specify a key file via '-signing-key' option " +
				"or use '-self-signed-ca'")
	}

	if opts.rootCertFile == "" {
		glog.Fatalf(
			"No root cert has been specified. Either specify a root cert file via '-root-cert' option " +
				"or use '-self-signed-ca'")
	}
}

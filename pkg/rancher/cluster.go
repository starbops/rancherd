package rancher

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"

	"github.com/sirupsen/logrus"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/rancher/rancherd/pkg/kubectl"
)

type Options struct {
	Kubeconfig string
}

// Update cluster client secret (fleet-local/local-kubeconfig):
// apiServerURL: value of Rancher setting "internal-api-url"
// apiServerCA: value of Rancher setting "internal-cacerts"
// Fleet needs these values to be set after Rancher v2.7.5 to provision a local cluster
func UpdateClientSecret(ctx context.Context, opts *Options) error {
	if opts == nil {
		opts = &Options{}
	}

	kubeconfig, err := kubectl.GetKubeconfig(opts.Kubeconfig)
	if err != nil {
		return err
	}

	conf, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return err
	}

	client := dynamic.NewForConfigOrDie(conf)
	settingClient := client.Resource(schema.GroupVersionResource{
		Group:    "management.cattle.io",
		Version:  "v3",
		Resource: "settings",
	})

	internalServerURLSetting, err := settingClient.Get(ctx, "internal-server-url", v1.GetOptions{})
	if err != nil {
		return err
	}
	internalServerURL := internalServerURLSetting.Object["value"].(string)
	logrus.Infof("internal-server-url is %q", internalServerURL)

	internalCACertSetting, err := settingClient.Get(ctx, "internal-cacerts", v1.GetOptions{})
	if err != nil {
		return err
	}
	internalCACerts := internalCACertSetting.Object["value"].(string)
	logrus.Infof("internal-cacerts is %q", internalCACerts)

	if internalServerURL == "" || internalCACerts == "" {
		return errors.New("Both 'internal-server-url' and 'internal-cacerts' settings must be configured")
	}

	data, err := ioutil.ReadFile(kubeconfig)
	if err != nil {
		return err
	}

	restConfig, err := clientcmd.RESTConfigFromKubeConfig(data)
	if err != nil {
		return err
	}

	k8s, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return err
	}

	secret, err := k8s.CoreV1().Secrets("fleet-local").Get(ctx, "local-kubeconfig", v1.GetOptions{})
	if err != nil {
		return err
	}

	toUpdate := secret.DeepCopy()
	toUpdate.Data["apiServerURL"] = []byte(internalServerURL)
	toUpdate.Data["apiServerCA"] = []byte(internalCACerts)
	_, err = k8s.CoreV1().Secrets("fleet-local").Update(ctx, toUpdate, v1.UpdateOptions{})

	if err == nil {
		fmt.Println("Cluster client secret is updated.")
	}

	return err
}

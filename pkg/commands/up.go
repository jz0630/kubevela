package commands

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/crossplane/oam-kubernetes-runtime/apis/core/v1alpha2"
	"github.com/ghodss/yaml"
	"github.com/kyokomi/emoji"
	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/api/types"
	"github.com/oam-dev/kubevela/pkg/appfile"
	"github.com/oam-dev/kubevela/pkg/appfile/template"
	"github.com/oam-dev/kubevela/pkg/application"
	cmdutil "github.com/oam-dev/kubevela/pkg/commands/util"
)

var (
	appFilePath string
	emojiRocket = emoji.Sprint(":rocket")
)

func NewUpCommand(c types.Args, ioStream cmdutil.IOStreams) *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "up",
		DisableFlagsInUseLine: true,
		Short:                 "Apply an appfile",
		Long:                  "Apply an appfile, by default vela.yaml",
		Annotations: map[string]string{
			types.TagCommandType: types.TypeStart,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			velaEnv, err := GetEnv(cmd)
			if err != nil {
				return err
			}
			kubecli, err := client.New(c.Config, client.Options{Scheme: c.Schema})
			if err != nil {
				return err
			}

			o := &appfileOptions{
				Kubecli: kubecli,
				IO:      ioStream,
				Env:     velaEnv,
			}
			filePath, err := cmd.Flags().GetString(appFilePath)
			if err != nil {
				return err
			}
			return o.Run(filePath)
		},
	}
	cmd.SetOut(ioStream.Out)

	cmd.Flags().StringP(appFilePath, "f", "", "specify file path for appfile")
	return cmd
}

type appfileOptions struct {
	Kubecli client.Client
	IO      cmdutil.IOStreams
	Env     *types.EnvMeta
}

func (o *appfileOptions) Run(filePath string) error {
	var app *appfile.AppFile
	var err error

	o.IO.Info("Parsing vela.yaml ...")
	if filePath != "" {
		app, err = appfile.LoadFromFile(filePath)
	} else {
		app, err = appfile.Load()
	}
	if err != nil {
		return err
	}

	o.IO.Info("Loading templates ...")
	tm, err := template.Load()
	if err != nil {
		return err
	}

	comps, appConfig, err := app.BuildOAM(o.Env.Namespace, o.IO, tm, false)
	if err != nil {
		return err
	}

	var cfg bytes.Buffer

	appConfig.TypeMeta = metav1.TypeMeta{
		APIVersion: v1alpha2.ApplicationConfigurationGroupVersionKind.GroupVersion().String(),
		Kind:       v1alpha2.ApplicationConfigurationKind,
	}
	b, err := yaml.Marshal(appConfig)
	if err != nil {
		return fmt.Errorf("marshal AppConfig failed: %w", err)
	}
	cfg.Write(b)
	cfg.WriteByte('\n')

	for _, comp := range comps {
		cfg.WriteString("---\n")
		comp.TypeMeta = metav1.TypeMeta{
			APIVersion: v1alpha2.ComponentGroupVersionKind.GroupVersion().String(),
			Kind:       v1alpha2.ComponentKind,
		}
		b, err := yaml.Marshal(comp)
		if err != nil {
			return fmt.Errorf("marshal service (%s) failed: %w", comp.Name, err)
		}
		cfg.Write(b)
		cfg.WriteByte('\n')
	}

	deployFilePath := ".vela/deploy.yaml"
	o.IO.Infof("writing deploy config to (%s)\n", deployFilePath)
	if err := os.MkdirAll(filepath.Dir(deployFilePath), 0700); err != nil {
		return err
	}
	if err := ioutil.WriteFile(deployFilePath, cfg.Bytes(), 0600); err != nil {
		return err
	}

	o.IO.Infof("\nApplying deploy configs ...\n")
	return o.ApplyAppConfig(appConfig, comps)
}

// Apply deploy config resources for the app.
// It differs by create and update:
// - for create, it displays app status along with information of url, metrics, ssh, logging.
// - for update, it rolls out a canary deployment and prints its information. User can verify the canary deployment.
//   This will wait for user approval. If approved, it continues upgrading the whole; otherwise, it would rollback.
func (o *appfileOptions) ApplyAppConfig(ac *v1alpha2.ApplicationConfiguration, comps []*v1alpha2.Component) error {
	key := apitypes.NamespacedName{
		Namespace: ac.Namespace,
		Name:      ac.Name,
	}
	o.IO.Infof("\nChecking if app has been deployed...\n")
	var tmpAC v1alpha2.ApplicationConfiguration
	err := o.Kubecli.Get(context.TODO(), key, &tmpAC)
	switch {
	case apierrors.IsNotFound(err):
		o.IO.Infof("app has not been deployed, creating a new deployment...\n")
	case err == nil:
		o.IO.Infof("app existed, updating existing deployment...\n")
	default:
		return err
	}
	if err := o.apply(ac, comps); err != nil {
		return err
	}
	o.info(ac.Name)
	return nil
}

func (o *appfileOptions) apply(ac *v1alpha2.ApplicationConfiguration, comps []*v1alpha2.Component) error {
	for _, comp := range comps {
		if err := application.CreateOrUpdateComponent(context.TODO(), o.Kubecli, comp); err != nil {
			return err
		}
	}
	return application.CreateOrUpdateAppConfig(context.TODO(), o.Kubecli, ac)
}

func (o *appfileOptions) info(name string) {
	o.IO.Infof("app has been deployed %s%s%s\n", emojiRocket, emojiRocket, emojiRocket)
	o.IO.Infof("\tURL: http://%s/\n", o.Env.Domain)
	o.IO.Infof("\tPort forward: vela listen %s <port>\n", name)
	o.IO.Infof("\tSSH: vela exec %s\n", name)
	o.IO.Infof("\tLogging: vela logs %s\n", name)
	o.IO.Infof("\tMetric: TODO\n")
}

package provision

import (
	"bytes"
	"fmt"
	"text/template"

	"github.com/docker/machine/libmachine/auth"
	"github.com/docker/machine/libmachine/engine"
	"github.com/docker/machine/libmachine/provision/pkgaction"
	"github.com/docker/machine/libmachine/swarm"
	"github.com/docker/machine/log"
	"github.com/docker/machine/utils"
)

type iRedhatFamilyProvisioner interface {
	// Useful process hooks that embedding structures can override
	PreProvisionHook() error
	PostProvisionHook() error
}

type RedhatFamilyProvisionerExt struct {
	DockerRPMPath 	string
	rhpi 			iRedhatFamilyProvisioner
}

type RedhatFamilyProvisioner struct {
	GenericProvisioner
	RedhatFamilyProvisionerExt
}

/* iRedhatFamilyProvisioner interface implementation */

func (provisioner *RedhatFamilyProvisioner) PreProvisionHook() error {
	return nil
}

func (provisioner *RedhatFamilyProvisioner) PostProvisionHook() error {
	return nil
}


/* Provision interface implementation */ 

func (provisioner *RedhatFamilyProvisioner) Service(name string, action pkgaction.ServiceAction) error {
	
	reloadDaemon := false
	switch action {
		case pkgaction.Start, pkgaction.Restart:
			reloadDaemon = true
	}
	
	// systemd needs reloaded when config changes on disk; we cannot
	// be sure exactly when it changes from the provisioner so
	// we call a reload on every restart to be safe
	if reloadDaemon {
		if _, err := provisioner.SSHCommand("sudo systemctl daemon-reload"); err != nil {
			return err
		}
	}
	
	command := fmt.Sprintf("sudo systemctl %s %s", action.String(), name)

	if _, err := provisioner.SSHCommand(command); err != nil {
		return err
	}

	return nil
}

func (provisioner *RedhatFamilyProvisioner) Package(name string, action pkgaction.PackageAction) error {
	var packageAction string

	switch action {
	case pkgaction.Install:
		packageAction = "install"
	case pkgaction.Remove:
		packageAction = "remove"
	case pkgaction.Upgrade:
		packageAction = "update" // TODO: Check that apt-get upgrade ~ yum update
	}

	command := fmt.Sprintf("sudo -E yum -y %s %s", packageAction, name)

	if _, err := provisioner.SSHCommand(command); err != nil {
		return err
	}

	return nil
}

func (provisioner *RedhatFamilyProvisioner) dockerDaemonResponding() bool {
	if _, err := provisioner.SSHCommand("sudo docker version"); err != nil {
		log.Warn("Error getting SSH command to check if the daemon is up: %s", err)
		return false
	}

	// The daemon is up if the command worked.  Carry on.
	return true
}

func (provisioner *RedhatFamilyProvisioner) Provision(swarmOptions swarm.SwarmOptions, authOptions auth.AuthOptions, engineOptions engine.EngineOptions) error {
	if provisioner.rhpi != nil {
		if err := provisioner.rhpi.PreProvisionHook(); err != nil {
			log.Debug("Pre-provision tasks failed.")
			return err
		}
	}
	
	provisioner.SwarmOptions = swarmOptions
	provisioner.AuthOptions = authOptions
	provisioner.EngineOptions = engineOptions

	// set default storage driver for redhat & friends
	if provisioner.EngineOptions.StorageDriver == "" {
		provisioner.EngineOptions.StorageDriver = "devicemapper"
	}

	if err := provisioner.SetHostname(provisioner.Driver.GetMachineName()); err != nil {
		return err
	}

	for _, pkg := range provisioner.Packages {
		log.Debugf("installing base package: name=%s", pkg)
		if err := provisioner.Package(pkg, pkgaction.Install); err != nil {
			return err
		}
	}

	// install docker
	if err := installDocker(provisioner); err != nil {
		return err
	}

	if err := utils.WaitFor(provisioner.dockerDaemonResponding); err != nil {
		return err
	}

	if err := makeDockerOptionsDir(provisioner); err != nil {
		return err
	}

	provisioner.AuthOptions = setRemoteAuthOptions(provisioner)

	if err := ConfigureAuth(provisioner); err != nil {
		return err
	}

	if err := configureSwarm(provisioner, swarmOptions); err != nil {
		return err
	}
	
	if provisioner.rhpi != nil {
		if err := provisioner.rhpi.PostProvisionHook(); err != nil {
			log.Debug("Post-provision tasks failed.")
			return err
		}
	}

	return nil
}

func (provisioner *RedhatFamilyProvisioner) GenerateDockerOptions(dockerPort int) (*DockerOptions, error) {
	var (
		engineCfg  bytes.Buffer
		configPath = provisioner.DaemonOptionsFile
	)

	// remove existing
	if _, err := provisioner.SSHCommand(fmt.Sprintf("sudo rm %s", configPath)); err != nil {
		return nil, err
	}

	driverNameLabel := fmt.Sprintf("provider=%s", provisioner.Driver.DriverName())
	provisioner.EngineOptions.Labels = append(provisioner.EngineOptions.Labels, driverNameLabel)

	// systemd / redhat will not load options if they are on newlines
	// instead, it just continues with a different set of options; yeah...
	engineConfigTmpl := `[Unit]
Description=Docker Application Container Engine
Documentation=http://docs.docker.com
After=network.target docker.socket
Required=docker.socket

[Service]
ExecStart=/usr/bin/docker -d -H tcp://0.0.0.0:{{.DockerPort}} -H unix:///var/run/docker.sock --storage-driver {{.EngineOptions.StorageDriver}} --tlsverify --tlscacert {{.AuthOptions.CaCertRemotePath}} --tlscert {{.AuthOptions.ServerCertRemotePath}} --tlskey {{.AuthOptions.ServerKeyRemotePath}} {{ range .EngineOptions.Labels }}--label {{.}} {{ end }}{{ range .EngineOptions.InsecureRegistry }}--insecure-registry {{.}} {{ end }}{{ range .EngineOptions.RegistryMirror }}--registry-mirror {{.}} {{ end }}{{ range .EngineOptions.ArbitraryFlags }}--{{.}} {{ end }}
MountFlags=slave
LimitNOFILE=1048576
LimitNPROC=1048576
LimitCORE=infinity

[Install]
WantedBy=multi-user.target
`
	t, err := template.New("engineConfig").Parse(engineConfigTmpl)
	if err != nil {
		return nil, err
	}

	engineConfigContext := EngineConfigContext{
		DockerPort:       dockerPort,
		AuthOptions:      provisioner.AuthOptions,
		EngineOptions:    provisioner.EngineOptions,
		DockerOptionsDir: provisioner.DockerOptionsDir,
	}

	t.Execute(&engineCfg, engineConfigContext)

	daemonOptsDir := configPath
	return &DockerOptions{
		EngineOptions:     engineCfg.String(),
		EngineOptionsPath: daemonOptsDir,
	}, nil
}


/* Methods common to all redhat-family distributions */
func installDocker(provisioner *RedhatFamilyProvisioner) error {
	if err := provisioner.installOfficialDocker(); err != nil {
		return err
	}

	if err := provisioner.Service("docker", pkgaction.Restart); err != nil {
		return err
	}

	if err := provisioner.Service("docker", pkgaction.Enable); err != nil {
		return err
	}

	return nil
}

func (provisioner *RedhatFamilyProvisioner) installOfficialDocker() error {
	log.Debug("installing docker")

	if _, err := provisioner.SSHCommand(fmt.Sprintf("sudo yum install -y --nogpgcheck  %s", provisioner.DockerRPMPath)); err != nil {
		return err
	}

	return nil
}





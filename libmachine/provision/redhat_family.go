package provision

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"github.com/docker/machine/libmachine/auth"
	"github.com/docker/machine/libmachine/engine"
	"github.com/docker/machine/libmachine/provision/pkgaction"
	"github.com/docker/machine/libmachine/swarm"
	"github.com/docker/machine/log"
	"github.com/docker/machine/utils"
)

type iRedhatFamilyProvisioner interface {
	// Useful process hooks
	PreProvisionHook() error
	PostProvisionHook() error
	PreGenerateDockerOptionsHook() error
	PostGenerateDockerOptionsHook() error
	//
	// Stuff that derived provisioners may want to override
	// Nothing (yet)
}

type RedhatFamilyProvisionerExt struct {
    SystemdEnabled 		bool
	DockerSysctlFile    string
	DockerPackageName   string
	DockerServiceName   string
	rhpi 				iRedhatFamilyProvisioner
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

func (provisioner *RedhatFamilyProvisioner) PreGenerateDockerOptionsHook() error {
	return nil
}

func (provisioner *RedhatFamilyProvisioner) PostGenerateDockerOptionsHook() error {
	return nil
}


/* Provision interface implementation */ 

func (provisioner *RedhatFamilyProvisioner) Service(name string, action pkgaction.ServiceAction) error {
	var command string; 
    
	// The command varies depending on whether the host is using sysvinit or systemd
	if provisioner.SystemdEnabled {
		command = fmt.Sprintf("sudo systemctl %s %s", action.String(), name)
	} else {
		switch action.String(){
			case "enable":
				command = fmt.Sprintf("sudo chkconfig --del %s", name)
				break;
			case "disable":
				command = fmt.Sprintf("sudo chkconfig --add %s", name)
				break;
			case "start", "stop", "restart":
				command = fmt.Sprintf("sudo service %s %s", name, action.String())
				break;
		}
	}	

	if _, err := provisioner.SSHCommand(command); err != nil {
		log.Debug(fmt.Sprintf("RedhatFamilyProvisioner.Service() -- command returned an error: %s", err))
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
		packageAction = "update" // TODO: Check that apt-get upgrade => yum update
	}

	command := fmt.Sprintf("sudo yum -y %s %s", packageAction, name)

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
			log.Debug("Pre-prevision tasks failed.")
			return err
		}
	}
	
	provisioner.SwarmOptions = swarmOptions
	provisioner.AuthOptions = authOptions
	provisioner.EngineOptions = engineOptions

	// set default storage driver for redhat/fedora/etc. 
	if provisioner.EngineOptions.StorageDriver == "" {
		provisioner.EngineOptions.StorageDriver = "devicemapper"
	}

	if err := provisioner.EnableIpForwarding(); err != nil {
		log.Debug("Attempt to enable IP forwarding failed. Docker may not be reachable.")
	}

	if err := provisioner.SetHostname(provisioner.Driver.GetMachineName()); err != nil {
		return err
	}

	if err := provisioner.CheckForSystemd(); err != nil {
		return err
	}

	for _, pkg := range provisioner.Packages {
		if err := provisioner.Package(pkg, pkgaction.Install); err != nil {
			return err
		}
	}

	if err := provisioner.InstallDocker(); err != nil {
		return err
	}

	if err := utils.WaitFor(provisioner.dockerDaemonResponding); err != nil {
		return err
	}

	if err := makeDockerOptionsDir(provisioner); err != nil {
		return err
	}

	if err := provisioner.ClearConfigFile(); err != nil {
		log.Debug("Failed to clear the existing config file")
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
			log.Debug("Post-prevision tasks failed.")
			return err
		}
	}

	return nil
}

func (provisioner *RedhatFamilyProvisioner) GenerateDockerOptions(dockerPort int) (*DockerOptions, error) {
	var (
		engineCfg bytes.Buffer
	)

	if provisioner.rhpi != nil {
		if err := provisioner.rhpi.PreGenerateDockerOptionsHook(); err != nil {
			log.Debug("RedhatFamilyProvisioner.GenerateDockerOptions() -- PreGenerateDockerOptionsTasks failed.")
			return nil, err
		}
	}
	
	driverNameLabel := fmt.Sprintf("provider=%s", provisioner.Driver.DriverName())
	provisioner.EngineOptions.Labels = append(provisioner.EngineOptions.Labels, driverNameLabel)

	engineConfigTmpl := `
OPTIONS='--selinux-enabled -H tcp://0.0.0.0:{{.DockerPort}} -H unix:///var/run/docker.sock --storage-driver {{.EngineOptions.StorageDriver}} --tlsverify --tlscacert {{.AuthOptions.CaCertRemotePath}} --tlscert {{.AuthOptions.ServerCertRemotePath}} --tlskey {{.AuthOptions.ServerKeyRemotePath}} {{ range .EngineOptions.Labels }}--label {{.}} {{ end }}{{ range .EngineOptions.InsecureRegistry }}--insecure-registry {{.}} {{ end }}{{ range .EngineOptions.RegistryMirror }}--registry-mirror {{.}} {{ end }}{{ range .EngineOptions.ArbitraryFlags }}--{{.}} {{ end }}'
DOCKER_CERT_PATH={{.DockerOptionsDir}}
ADD_REGISTRY=''
GOTRACEBACK='crash'
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
	
	if provisioner.rhpi != nil {
		if err := provisioner.rhpi.PostGenerateDockerOptionsHook(); err != nil {
			log.Debug("RedhatFamilyProvisioner.GenerateDockerOptions() -- PostGenerateDockerOptionsTasks failed.")
			return nil, err
		}
	}
	
	return &DockerOptions{
		EngineOptions:     engineCfg.String(),
		EngineOptionsPath: provisioner.DaemonOptionsFile,
	}, nil
}


/* Methods common to all redhat-family distributions */

func (provisioner *RedhatFamilyProvisioner) InstallDocker() error {
	if err := provisioner.Package(provisioner.DockerPackageName, pkgaction.Install); err != nil {
		return err
	}

	if err := provisioner.Service(provisioner.DockerServiceName, pkgaction.Restart); err != nil {
		return err
	}

	if err := provisioner.Service(provisioner.DockerServiceName, pkgaction.Enable); err != nil {
		return err
	}

	return nil
}

func (provisioner *RedhatFamilyProvisioner) CheckForSystemd() error {
	var (
		command 	string
		reader		bytes.Buffer
	)

	// Test for sysvint or something else (i.e. systemd on redhat/fedora)
	command = "pidof /sbin/init &>/dev/null && echo sysvinit || echo systemd" 
	response, err := provisioner.SSHCommand(command)
	if err != nil {
		return err
	}
	
	if _, err := reader.ReadFrom(response.Stdout); err != nil {
		return err
	}
	
	result := reader.String()
	if strings.TrimSpace(result) == "systemd" {
		provisioner.SystemdEnabled = true
	} else {
		provisioner.SystemdEnabled = false
	}

	return nil
}

func (provisioner *RedhatFamilyProvisioner ) ClearConfigFile() error {
	var command string
	
	//command = fmt.Sprintf("sudo sh -c ': > %s'", provisioner.DaemonOptionsFile)
	command = fmt.Sprintf("sudo rm -f %s", provisioner.DaemonOptionsFile)
	if _, err := provisioner.SSHCommand(command); err != nil {
		log.Debug("Failed to run SSH command to clear existing config file")
		return err
	}

	return nil
}

func (provisioner *RedhatFamilyProvisioner) EnableIpForwarding() error {
	var command string
	
	// TODO: Is this in effect the same as passing docker-machine the --ip-forward=true flag? 
	
	// Command to enable IP forwarding 
	command = "sudo sysctl -w net.ipv4.ip_forward=1"
	if _, err := provisioner.SSHCommand(command); err != nil {
		log.Debug("Failed to run SSH command to enable ip forwarding to docker containers")
		return err
	}
	
	// Command to persist enablement of IP forwarding between boots
	command = fmt.Sprintf("sudo sh -c 'echo net.ipv4.ip_forward = 1 >> %s'", provisioner.DockerSysctlFile)
	if _, err := provisioner.SSHCommand(command); err != nil {
		log.Debug("Failed to run SSH command to make ip forwarding to docker containers permanent")
		return err
	}
	
	return nil
}





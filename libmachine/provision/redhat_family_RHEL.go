package provision

import (
	"fmt"

	"github.com/docker/machine/drivers"
	"github.com/docker/machine/libmachine/provision/pkgaction"
	"github.com/docker/machine/log"
	
)

const (
	// these are the custom dyn builds
	dockerURL     = "https://docker-mcn.s3.amazonaws.com/public/redhat/1.6.0/dynbinary/docker-1.6.0"
	dockerinitURL = "https://docker-mcn.s3.amazonaws.com/public/redhat/1.6.0/dynbinary/dockerinit-1.6.0"
)

func init() {
	Register("RedHat", &RegisteredProvisioner{
		New: NewRedHatProvisioner,
	})
}

func NewRedHatProvisioner(d drivers.Driver) Provisioner {
	rp := RedHatProvisioner{
		RedhatFamilyProvisioner{
			GenericProvisioner{
				DockerOptionsDir:  "/etc/docker",
				DaemonOptionsFile: "/etc/sysconfig/docker",
				OsReleaseId:       "rhel",
				Packages: []string{
					"curl",
				},
				Driver: d,
			},
			RedhatFamilyProvisionerExt{
			    SystemdEnabled: 	true,
				DockerPackageName:  "docker-io",
				DockerServiceName:  "docker",
				DockerSysctlFile:   "/etc/sysctl.d/80-docker.conf",
				rhpi: nil,
			},
		},
	}
	rp.rhpi = &rp // Point the rhpi interface to ourself
	return &rp
}

type RedHatProvisioner struct {
	RedhatFamilyProvisioner
}


/* iRedhatFamilyProvisioner interface -- overrides */
func (provisioner *RedHatProvisioner) PrePrevisionHook() error {
	// setup extras repo
	if err := provisioner.configureRepos(); err != nil {
		return err
	}
	return nil
}

func (provisioner *RedHatProvisioner) PostPrevisionHook() error {
	// TODO: Not sure why we do this, given docker is installed from
	// packages by InstallDocker(), but it was in the redhat-provisioning 
	// branch of https://github.com/ehazlett/machine.git
	// Is it more of a general use case, or a personal one? 
	if err := provisioner.installOfficialDocker(); err != nil {
		return err
	}
	return nil
}


/* Provision interface */
// Nothing to do! Everything unique to RedHat is handled in the 
// iRedhatFamilyProvisioner hooks above.  Yay. 


/* Methods that are unique to Redhat */
func (provisioner *RedHatProvisioner) isAWS() bool {
	if _, err := provisioner.SSHCommand("curl -s http://169.254.169.254/latest/meta-data/ami-id"); err != nil {
		return false
	}

	return true
}

func (provisioner *RedHatProvisioner) configureRepos() error {

	// TODO: should this be handled differently? on aws we need to enable
	// the extras repo different than a standalone rhel box

	log.Debug("configuring extra repo")
	repoCmd := "subscription-manager repos --enable=rhel-7-server-extras-rpms"
	if provisioner.isAWS() {
		repoCmd = "yum-config-manager --enable rhui-REGION-rhel-server-extras"
	}

	if _, err := provisioner.SSHCommand(fmt.Sprintf("sudo %s", repoCmd)); err != nil {
		return err
	}

	return nil
}

func (provisioner *RedHatProvisioner) installOfficialDocker() error {
	log.Debug("installing official Docker binary")

	if err := provisioner.Service("docker", pkgaction.Stop); err != nil {
		return err
	}

	// TODO: replace with Docker RPMs when they are ready
	if _, err := provisioner.SSHCommand(fmt.Sprintf("sudo -E curl -o /usr/bin/docker %s", dockerURL)); err != nil {
		return err
	}

	if _, err := provisioner.SSHCommand(fmt.Sprintf("sudo -E curl -o /usr/libexec/docker/dockerinit %s", dockerinitURL)); err != nil {
		return err
	}

	if err := provisioner.Service("docker", pkgaction.Restart); err != nil {
		return err
	}

	return nil
}


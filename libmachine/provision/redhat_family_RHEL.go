package provision

import (
	"fmt"

	"github.com/docker/machine/drivers"
	"github.com/docker/machine/log"
)

const (
	// TODO: eventually the RPM install process will be integrated
	// into the get.docker.com install script; for now
	// we install via vendored RPMs
	dockerRHELRPMPath = "https://docker-mcn.s3.amazonaws.com/public/redhat/rpms/docker-engine-1.6.1-0.0.20150511.171646.git1b47f9f.el7.centos.x86_64.rpm"
)

func init() {
	Register("RedHat", &RegisteredProvisioner{
		New: NewRhelProvisioner,
	})
}

func NewRhelProvisioner(d drivers.Driver) Provisioner {
	g := GenericProvisioner{
			DockerOptionsDir:  "/etc/docker",
			DaemonOptionsFile: "/lib/systemd/system/docker.service",
			OsReleaseId:       "rhel",
			Packages: []string{
				"curl",
			},
			Driver: d,
		}
	rfe := RedhatFamilyProvisionerExt{
			DockerRPMPath:		dockerRHELRPMPath,
			rhpi: 				nil,
		}
	p := RhelProvisioner{
		RedhatFamilyProvisioner{
			g,
			rfe,
		},
	}
	
	p.rhpi = &p // Point the rhpi interface the RhelProvisioner
	
	return &p
}

type RhelProvisioner struct {
	RedhatFamilyProvisioner
}


/* iRedhatFamilyProvisioner interface -- overrides */
func (provisioner *RhelProvisioner) PreProvisionHook() error {
	// setup extras repo
	if err := provisioner.configureRepos(); err != nil {
		return err
	}
	return nil
}


/* Methods that are unique to RHEL */
func (provisioner *RhelProvisioner) isAWS() bool {
	if _, err := provisioner.SSHCommand("curl -s http://169.254.169.254/latest/meta-data/ami-id"); err != nil {
		return false
	}

	return true
}

func (provisioner *RhelProvisioner) configureRepos() error {
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


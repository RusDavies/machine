package provision

import (
	"github.com/docker/machine/drivers"
)

func init() {
	Register("Fedora", &RegisteredProvisioner{
		New: NewFedoraProvisioner,
	})
}

func NewFedoraProvisioner(d drivers.Driver) Provisioner {
	fp := FedoraProvisioner{
		RedhatFamilyProvisioner{
			GenericProvisioner {
				DockerOptionsDir:  "/etc/docker",
				DaemonOptionsFile: "/etc/sysconfig/docker",
				OsReleaseId:       "fedora",
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
	fp.rhpi = &fp  // Point the rhpi interface to ourself
	return &fp
}

type FedoraProvisioner struct {
	RedhatFamilyProvisioner
}


/* iRedhatFamilyProvisioner interface -- overrides */
// Nothing (yet). 

/* Provision interface implementation */ 
// Nothing (yet).  

/* Methods that are unique to Fedora */
// Nothing (yet).


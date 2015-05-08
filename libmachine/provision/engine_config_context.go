package provision

import (
	"github.com/docker/machine/libmachine/auth"
	"github.com/docker/machine/libmachine/engine"
)

type EngineConfigContext struct {
	DockerOptsEnvVar	string
	DockerPort    		int
	AuthOptions   		auth.AuthOptions
	EngineOptions 		engine.EngineOptions
}

package main

import (
	"github.com/kronaxis/agent-gateway/accounts"
	"github.com/kronaxis/agent-gateway/registry"
)

// Type aliases keep the rest of the main package readable while still
// referencing the types defined in the sub-packages.
type registryDeps = registry.Registry
type accountsDeps = accounts.Manager

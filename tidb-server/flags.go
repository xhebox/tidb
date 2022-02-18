package main

type ExclusieBool struct {
	enable  bool
	disable bool
}

func (c ExclusieBool) Enable() bool {
	return c.enable && !c.disable
}

type Parameters struct {
	v bool

	configPath   string
	configCheck  bool
	configStrict bool

	// log
	logLevel         string
	logFile          string
	logSlowQueryFile string

	// status
	statusEnable bool
	statusHost   string
	statusPort   string
	cors         string

	// metrics
	metricsAddr     string
	metricsInterval uint

	// proxy
	proxyProtocolNetworks      string
	proxyProtocolHeaderTimeout uint

	// security
	securityEnable ExclusieBool

	// sql
	host             string
	advertiseAddress string
	port             string
	socket           string

	// store
	store     string
	storePath string

	// plugin
	pluginDir  string
	pluginLoad string

	// repair
	repairMode bool
	repairList string
	repairTLS  bool

	// flags
	binlog      bool
	ddlWoker    bool
	ddlLease    string
	tokenLimit  int
	affinityCPU string
}


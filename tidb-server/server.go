package main

import (
	"github.com/spf13/cobra"
)

func NewRootCmd(flags *Parameters) *cobra.Command {
	rootCmd := &cobra.Command{
		Short: "Hugo is a very fast static site generator",
		Long: `A Fast and Flexible Static Site Generator built with
                love by spf13 and friends in Go.
                Complete documentation is available at http://hugo.spf13.com`,
	}

	rootCmd.PersistentFlags().BoolVar(&flags.v, "V", false, "print version information and exit")

	rootCmd.PersistentFlags().StringVar(&flags.configPath, "config", "", "config file path")
	rootCmd.PersistentFlags().BoolVar(&flags.configCheck, "config-check", false, "validate config file and exit")
	rootCmd.PersistentFlags().BoolVar(&flags.configStrict, "config-strict", false, "enforce config file validity")

	// log
	rootCmd.PersistentFlags().StringVarP(&flags.logLevel, "log-level", "L", "info", "log level: info, debug, warn, error, fatal")
	rootCmd.PersistentFlags().StringVar(&flags.logFile, "log-file", "", "log file path, defaults to stdout")
	rootCmd.PersistentFlags().StringVar(&flags.logSlowQueryFile, "log-slow-query", "", "slow query log file path")

	// status
	rootCmd.PersistentFlags().BoolVar(&flags.statusEnable, "report-status", true, "whether enable status report HTTP service")
	rootCmd.PersistentFlags().StringVar(&flags.statusHost, "status-host", "0.0.0.0", "tidb server status host")
	rootCmd.PersistentFlags().StringVar(&flags.statusPort, "status", "10086", "tidb server status port")
	rootCmd.PersistentFlags().StringVar(&flags.cors, "cors", "", "tidb server allow cors origin")

	// metrics
	rootCmd.PersistentFlags().StringVar(&flags.metricsAddr, "metrics-addr", "", "prometheus pushgateway address, leaves it empty will disable prometheus push")
	rootCmd.PersistentFlags().UintVar(&flags.metricsInterval, "metrics-interval", 15, "prometheus client push interval in second, set \"0\" to disable prometheus push")

	// proxy
	rootCmd.PersistentFlags().StringVar(&flags.proxyProtocolNetworks, "proxy-protocol-network", "", "proxy protocol networks allowed IP or *, empty mean disable proxy protocol support")
	rootCmd.PersistentFlags().UintVar(&flags.proxyProtocolHeaderTimeout, "proxy-protocol-header-timeout", 5, "proxy protocol header read timeout, unit is second.")

	// security
	rootCmd.PersistentFlags().BoolVar(&flags.securityEnable.enable, "initialize-secure", false, "bootstrap in secure mode")
	rootCmd.PersistentFlags().BoolVar(&flags.securityEnable.disable, "initialize-insecure", true, "bootstrap in insecure mode")

	// sql
	rootCmd.PersistentFlags().StringVar(&flags.host, "host", "", "tidb server host")
	rootCmd.PersistentFlags().StringVar(&flags.advertiseAddress, "advertise-address", "", "tidb server advertise IP")
	rootCmd.PersistentFlags().StringVarP(&flags.port, "port", "P", "4000", "tidb server port")
	rootCmd.PersistentFlags().StringVar(&flags.socket, "socket", "/tmp/tidb-{Port}.sock", "The socket file to use for connection")

	return rootCmd
}

package serv

import (
	"github.com/spf13/cobra"
)

func cmdServ(cmd *cobra.Command, args []string) {
	var err error

	db, err = initDBPool(conf)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to connect to database")
	}

	initCompiler()
	initAllowList(confPath)
	initPreparedList()
	initWatcher(confPath)

	startHTTP()
}

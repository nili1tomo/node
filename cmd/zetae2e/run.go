package main

import (
	"context"
	"errors"
	"time"

	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/zeta-chain/zetacore/app"
	zetae2econfig "github.com/zeta-chain/zetacore/cmd/zetae2e/config"
	"github.com/zeta-chain/zetacore/contrib/localnet/orchestrator/smoketest/config"
	"github.com/zeta-chain/zetacore/contrib/localnet/orchestrator/smoketest/runner"
	"github.com/zeta-chain/zetacore/contrib/localnet/orchestrator/smoketest/smoketests"
	"github.com/zeta-chain/zetacore/contrib/localnet/orchestrator/smoketest/utils"
)

const flagVerbose = "verbose"

const FungibleAdminMnemonic = "snow grace federal cupboard arrive fancy gym lady uniform rotate exercise either leave alien grass" // #nosec G101 - used for testing

// NewRunCmd returns the run command
// which runs the smoketest from a config file describing the tests, networks, and accounts
func NewRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run [config-file]",
		Short: "Run E2E tests from a config file",
		RunE:  runE2ETest,
		Args:  cobra.ExactArgs(1),
	}
	cmd.Flags().Bool(
		flagVerbose,
		false,
		"set to true to enable verbose logging",
	)

	return cmd
}

func runE2ETest(cmd *cobra.Command, args []string) error {
	// read the config file
	conf, err := config.ReadConfig(args[0])
	if err != nil {
		return err
	}

	// read flag
	verbose, err := cmd.Flags().GetBool(flagVerbose)
	if err != nil {
		return err
	}

	// initialize logger
	logger := runner.NewLogger(verbose, color.FgWhite, "e2e")

	// set config
	app.SetConfig()

	// initialize context
	ctx, cancel := context.WithCancel(context.Background())

	// get EVM address from config
	evmAddr := conf.Accounts.EVMAddress
	if ethcommon.IsHexAddress(evmAddr) {
		cancel()
		return errors.New("invalid EVM address")
	}

	// initialize deployer runner with config
	testRunner, err := zetae2econfig.RunnerFromConfig(
		ctx,
		"e2e",
		cancel,
		conf,
		ethcommon.HexToAddress(evmAddr),
		conf.Accounts.EVMPrivKey,
		utils.FungibleAdminName,
		FungibleAdminMnemonic,
		logger,
	)
	if err != nil {
		cancel()
		return err
	}

	testStartTime := time.Now()
	logger.Print("starting tests")

	// fetch the TSS address
	testRunner.SetTSSAddresses()

	// run tests
	if err := testRunner.RunSmokeTestsFromNames(
		smoketests.AllSmokeTests,
		conf.TestList...,
	); err != nil {
		cancel()
		return err
	}

	logger.Print("tests finished successfully in %s", time.Since(testStartTime).String())

	return nil
}

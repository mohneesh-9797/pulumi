// Copyright 2016-2017, Pulumi Corporation.  All rights reserved.

package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pulumi/pulumi/pkg/util/contract"

	"github.com/pulumi/pulumi/pkg/pack"
	"github.com/pulumi/pulumi/pkg/resource/config"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	"github.com/pulumi/pulumi/pkg/tokens"
	"github.com/pulumi/pulumi/pkg/util/cmdutil"
)

func newConfigCmd() *cobra.Command {
	var stack string
	var showSecrets bool

	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage configuration",
		Long: "Lists all configuration values for a specific stack. To add a new configuration value, run\n" +
			"'pulumi config set', to remove and existing value run 'pulumi config rm'. To get the value of\n" +
			"for a specific configuration key, use 'pulumi config get <key-name>'.",
		Args: cmdutil.NoArgs,
		Run: cmdutil.RunFunc(func(cmd *cobra.Command, args []string) error {
			stackName, err := explicitOrCurrent(stack, backend)
			if err != nil {
				return err
			}

			return listConfig(stackName, showSecrets)
		}),
	}

	cmd.Flags().BoolVar(
		&showSecrets, "show-secrets", false,
		"Show secret values when listing config instead of displaying blinded values")
	cmd.PersistentFlags().StringVarP(
		&stack, "stack", "s", "",
		"Operate on a different stack than the currently selected stack")

	cmd.AddCommand(newConfigGetCmd(&stack))
	cmd.AddCommand(newConfigRmCmd(&stack))
	cmd.AddCommand(newConfigSetCmd(&stack))

	return cmd
}

func newConfigGetCmd(stack *string) *cobra.Command {
	getCmd := &cobra.Command{
		Use:   "get <key>",
		Short: "Get a single configuration value",
		Args:  cmdutil.ExactArgs(1),
		Run: cmdutil.RunFunc(func(cmd *cobra.Command, args []string) error {
			stackName, err := explicitOrCurrent(*stack, backend)
			if err != nil {
				return err
			}

			key, err := parseConfigKey(args[0])
			if err != nil {
				return errors.Wrap(err, "invalid configuration key")
			}
			return getConfig(stackName, key)
		}),
	}

	return getCmd
}

func newConfigRmCmd(stack *string) *cobra.Command {
	var all bool
	var save bool

	rmCmd := &cobra.Command{
		Use:   "rm <key>",
		Short: "Remove configuration value",
		Args:  cmdutil.ExactArgs(1),
		Run: cmdutil.RunFunc(func(cmd *cobra.Command, args []string) error {
			if all && *stack != "" {
				return errors.New("if --all is specified, an explicit stack can not be provided")
			}

			var stackName tokens.QName
			if !all {
				var err error
				if stackName, err = explicitOrCurrent(*stack, backend); err != nil {
					return err
				}
			}

			key, err := parseConfigKey(args[0])
			if err != nil {
				return errors.Wrap(err, "invalid configuration key")
			}

			if save {
				return deleteProjectConfiguration(stackName, key)
			}

			return deleteWorkspaceConfiguration(stackName, key)
		}),
	}

	rmCmd.PersistentFlags().BoolVar(
		&save, "save", false,
		"Remove the configuration value in the project file instead instead of a locally set value")
	rmCmd.PersistentFlags().BoolVar(
		&all, "all", false,
		"Remove a project wide configuration value that applies to all stacks")

	return rmCmd
}

func newConfigSetCmd(stack *string) *cobra.Command {
	var all bool
	var save bool
	var secret bool

	setCmd := &cobra.Command{
		Use:   "set <key> [value]",
		Short: "Set configuration value",
		Args:  cmdutil.RangeArgs(1, 2),
		Run: cmdutil.RunFunc(func(cmd *cobra.Command, args []string) error {
			if all && *stack != "" {
				return errors.New("if --all is specified, an explicit stack can not be provided")
			}

			var stackName tokens.QName
			if !all {
				var err error
				if stackName, err = explicitOrCurrent(*stack, backend); err != nil {
					return err
				}
			}

			key, err := parseConfigKey(args[0])
			if err != nil {
				return errors.Wrap(err, "invalid configuration key")
			}

			var c config.ValueEncrypter
			if secret {
				c, err = getSymmetricCrypter()
				if err != nil {
					return err
				}
			}

			var value string
			if len(args) == 2 {
				value = args[1]
			} else if !secret {
				value, err = readConsole("value")
				if err != nil {
					return err
				}
			} else {
				value, err = readConsoleNoEchoWithPrompt("value")
				if err != nil {
					return err
				}
			}

			if !secret {
				err = setConfiguration(stackName, key, config.NewValue(value), save)
				if err != nil {
					return err
				}
				fmt.Printf("Set key '%s' with value '%s' as plaintext\n", args[0], value)
				return nil
			}

			enc, err := c.EncryptValue(value)
			if err != nil {
				return err
			}

			err = setConfiguration(stackName, key, config.NewSecureValue(enc), save)
			if err != nil {
				return err
			}

			fmt.Printf("Set key '%s' with with encrypted value\n", args[0])
			return nil
		}),
	}

	setCmd.PersistentFlags().BoolVar(
		&secret, "secret", false,
		"Encrypt the value instead of storing it in plaintext")
	setCmd.PersistentFlags().BoolVar(
		&save, "save", false,
		"Save the configuration value in the project file instead of locally")
	setCmd.PersistentFlags().BoolVar(
		&all, "all", false,
		"Set a configuration value for all stacks for this project")

	return setCmd
}

func parseConfigKey(key string) (tokens.ModuleMember, error) {
	// As a convience, we'll treat any key with no delimiter as if:
	// <program-name>:config:<key> had been written instead
	if !strings.Contains(key, tokens.TokenDelimiter) {
		pkg, err := getPackage()
		if err != nil {
			return "", err
		}

		return tokens.ParseModuleMember(fmt.Sprintf("%s:config:%s", pkg.Name, key))
	}

	return tokens.ParseModuleMember(key)
}

func prettyKey(key string) string {
	pkg, err := getPackage()
	if err != nil {
		return key
	}

	return prettyKeyForPackage(key, pkg)
}

func prettyKeyForPackage(key string, pkg *pack.Package) string {
	s := key
	defaultPrefix := fmt.Sprintf("%s:config:", pkg.Name)

	if strings.HasPrefix(s, defaultPrefix) {
		return s[len(defaultPrefix):]
	}

	return s
}

func setConfiguration(stackName tokens.QName, key tokens.ModuleMember, value config.Value, save bool) error {
	if save {
		return setProjectConfiguration(stackName, key, value)
	}

	return setWorkspaceConfiguration(stackName, key, value)
}

func listConfig(stackName tokens.QName, showSecrets bool) error {
	cfg, err := getConfiguration(stackName)
	if err != nil {
		return err
	}

	var decrypter config.ValueDecrypter = blindingDecrypter{}

	if hasSecureValue(cfg) && showSecrets {
		decrypter, err = getSymmetricCrypter()
		if err != nil {
			return err
		}
	}

	if cfg != nil {
		fmt.Printf("%-32s %-32s\n", "KEY", "VALUE")
		var keys []string
		for key := range cfg {
			// Note that we use the fully qualified module member here instead of a `prettyKey`, this lets us ensure
			// that all the config values for the current program are displayed next to one another in the output.
			keys = append(keys, string(key))
		}
		sort.Strings(keys)
		for _, key := range keys {
			decrypted, err := cfg[tokens.ModuleMember(key)].Value(decrypter)
			if err != nil {
				return errors.Wrap(err, "could not decrypt configuration value")
			}

			fmt.Printf("%-32s %-32s\n", prettyKey(key), decrypted)
		}
	}

	return nil
}

func getConfig(stackName tokens.QName, key tokens.ModuleMember) error {
	cfg, err := getConfiguration(stackName)
	if err != nil {
		return err
	}

	if cfg != nil {
		if v, ok := cfg[key]; ok {
			var decrypter config.ValueDecrypter = panicCrypter{}

			if v.Secure() {
				decrypter, err = getSymmetricCrypter()
				if err != nil {
					return err
				}
			}

			decrypted, err := v.Value(decrypter)
			if err != nil {
				return errors.Wrap(err, "could not decrypt configuation value")
			}

			fmt.Printf("%v\n", decrypted)

			return nil
		}
	}

	return errors.Errorf("configuration key '%v' not found for stack '%v'", prettyKey(key.String()), stackName)
}

func getConfiguration(stackName tokens.QName) (map[tokens.ModuleMember]config.Value, error) {
	contract.Require(stackName != "", "stackName")

	workspace, err := newWorkspace()
	if err != nil {
		return nil, err
	}

	pkg, err := getPackage()
	if err != nil {
		return nil, err
	}

	configs := make([]map[tokens.ModuleMember]config.Value, 4)
	configs = append(configs, pkg.Config)

	if stackInfo, has := pkg.Stacks[stackName]; has {
		configs = append(configs, stackInfo.Config)
	}

	if localAllStackConfig, has := workspace.Settings().Config[""]; has {
		configs = append(configs, localAllStackConfig)
	}

	if localStackConfig, has := workspace.Settings().Config[stackName]; has {
		configs = append(configs, localStackConfig)
	}

	return mergeConfigs(configs...), nil
}

func deleteProjectConfiguration(stackName tokens.QName, key tokens.ModuleMember) error {
	pkg, err := getPackage()
	if err != nil {
		return err
	}

	if stackName == "" {
		if pkg.Config != nil {
			delete(pkg.Config, key)
		}
	} else {
		if pkg.Stacks[stackName].Config != nil {
			delete(pkg.Stacks[stackName].Config, key)
		}
	}

	return savePackage(pkg)
}

func deleteWorkspaceConfiguration(stackName tokens.QName, key tokens.ModuleMember) error {
	workspace, err := newWorkspace()
	if err != nil {
		return err
	}

	if config, has := workspace.Settings().Config[stackName]; has {
		delete(config, key)
	}

	return workspace.Save()
}

func setProjectConfiguration(stackName tokens.QName, key tokens.ModuleMember, value config.Value) error {
	pkg, err := getPackage()
	if err != nil {
		return err
	}

	if stackName == "" {
		if pkg.Config == nil {
			pkg.Config = make(map[tokens.ModuleMember]config.Value)
		}

		pkg.Config[key] = value
	} else {
		if pkg.Stacks == nil {
			pkg.Stacks = make(map[tokens.QName]pack.StackInfo)
		}

		if pkg.Stacks[stackName].Config == nil {
			si := pkg.Stacks[stackName]
			si.Config = make(map[tokens.ModuleMember]config.Value)
			pkg.Stacks[stackName] = si
		}

		pkg.Stacks[stackName].Config[key] = value
	}

	return savePackage(pkg)
}

func setWorkspaceConfiguration(stackName tokens.QName, key tokens.ModuleMember, value config.Value) error {
	workspace, err := newWorkspace()
	if err != nil {
		return err
	}

	if _, has := workspace.Settings().Config[stackName]; !has {
		workspace.Settings().Config[stackName] = make(map[tokens.ModuleMember]config.Value)
	}

	workspace.Settings().Config[stackName][key] = value

	return workspace.Save()
}

func mergeConfigs(configs ...map[tokens.ModuleMember]config.Value) map[tokens.ModuleMember]config.Value {
	merged := make(map[tokens.ModuleMember]config.Value)

	for _, config := range configs {
		for key, value := range config {
			merged[key] = value
		}
	}

	return merged
}

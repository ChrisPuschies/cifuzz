package login

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pkg/browser"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/term"

	"code-intelligence.com/cifuzz/internal/access_tokens"
	"code-intelligence.com/cifuzz/internal/api"
	"code-intelligence.com/cifuzz/internal/cmdutils"
	"code-intelligence.com/cifuzz/pkg/dialog"
	"code-intelligence.com/cifuzz/pkg/log"
)

type loginOpts struct {
	Interactive bool   `mapstructure:"interactive"`
	Server      string `mapstructure:"server"`
}

type loginCmd struct {
	opts *loginOpts
}

func New() *cobra.Command {
	var bindFlags func()

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate with a CI Fuzz Server instance",
		Long: `This command is used to authenticate with a CI Fuzz Server instance.
To learn more, visit https://www.code-intelligence.com.`,
		Example: "$ cifuzz login",
		PreRunE: func(cmd *cobra.Command, args []string) error {
			// Bind viper keys to flags. We can't do this in the New
			// function, because that would re-bind viper keys which
			// were bound to the flags of other commands before.
			bindFlags()
			return nil
		},
		RunE: func(c *cobra.Command, args []string) error {
			opts := &loginOpts{
				Interactive: viper.GetBool("interactive"),
				Server:      viper.GetString("server"),
			}

			// Check if the server option is a valid URL
			err := api.ValidateURL(opts.Server)
			if err != nil {
				// See if prefixing https:// makes it a valid URL
				err = api.ValidateURL("https://" + opts.Server)
				if err != nil {
					log.Error(err, fmt.Sprintf("server %q is not a valid URL", opts.Server))
				}
				opts.Server = "https://" + opts.Server
			}

			cmd := loginCmd{opts: opts}
			return cmd.run()
		},
	}
	bindFlags = cmdutils.AddFlags(cmd,
		cmdutils.AddInteractiveFlag,
		cmdutils.AddServerFlag,
	)

	cmdutils.DisableConfigCheck(cmd)

	return cmd
}

func (c *loginCmd) run() error {
	// Obtain the API access token
	var token string

	// First, if stdin is *not* a TTY, we try to read it from stdin,
	// in case it was provided via `cifuzz login < token-file`
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		// This should never block because stdin is not a TTY.
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return errors.WithStack(err)
		}
		token = strings.TrimSpace(string(b))
	}

	// Try the environment variable
	if token == "" {
		token = os.Getenv("CIFUZZ_API_TOKEN")
	}

	// Try the access tokens config file
	if token == "" {
		token = access_tokens.Get(c.opts.Server)
		if token != "" {
			return c.handleExistingToken(token)
		}
	}

	// Try reading it interactively
	if token == "" && c.opts.Interactive && term.IsTerminal(int(os.Stdin.Fd())) {
		msg := fmt.Sprintf(`Enter an API access token and press Enter. You can generate a token for
your account at %s/dashboard/settings/account/tokens?create.`+"\n", c.opts.Server)

		err := browser.OpenURL(c.opts.Server + "/dashboard/settings/account/tokens?create")
		if err != nil {
			log.Error(err, "failed to open browser")
		}

		token, err = dialog.ReadSecret(msg, os.Stdin)
		if err != nil {
			return err
		}
	}

	if token == "" {
		err := errors.New(`No API access token provided. Please pass a valid token via stdin,
the CIFUZZ_API_TOKEN environment variable or run in interactive mode.`)
		return cmdutils.WrapIncorrectUsageError(err)
	}

	return c.handleNewToken(token)
}

func (c *loginCmd) handleNewToken(token string) error {
	// Try to authenticate with the access token
	tokenValid, err := cmdutils.IsTokenValid(c.opts.Server, token)
	if err != nil {
		return err
	}
	if !tokenValid {
		return errors.New("failed to authenticate with the provided API access token")
	}

	// Store the access token in the config file
	err = access_tokens.Set(c.opts.Server, token)
	if err != nil {
		return err
	}

	log.Successf("Successfully authenticated with %s", c.opts.Server)
	return nil
}

func (c *loginCmd) handleExistingToken(token string) error {
	tokenValid, err := cmdutils.IsTokenValid(c.opts.Server, token)
	if err != nil {
		return err
	}
	if !tokenValid {
		log.Warnf(`cifuzz detected an API access token, but failed to authenticate with it.
This might happen if the token has been revoked.
Please remove the token from %s and try again.`,
			access_tokens.GetTokenFilePath())
		return cmdutils.WrapSilentError(errors.New("failed to authenticate with the provided API access token"))
	}
	log.Success("You are already logged in.")
	log.Infof("Your API access token is stored in %s", access_tokens.GetTokenFilePath())
	return nil
}

package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"shelley.exe.dev/llm/llmhttp"
	"shelley.exe.dev/llm/oauth"
)

// parseLoginArgs validates the provider argument for `shelley login`.
func parseLoginArgs(args []string) (string, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("usage: shelley login <anthropic|openai>")
	}
	switch args[0] {
	case "anthropic", "openai":
		return args[0], nil
	default:
		return "", fmt.Errorf("unsupported provider %q (supported: anthropic, openai)", args[0])
	}
}

// loginFlow is the provider-agnostic shape of an interactive OAuth login.
type loginFlow interface {
	AuthorizeURL() string
	Complete(ctx context.Context, code string) error
}

// runLogin runs the interactive OAuth login flow for a subscription provider.
func runLogin(args []string) {
	provider, err := parseLoginArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	store := &oauth.Store{Path: oauth.DefaultCredentialsPath()}
	httpc := llmhttp.NewClient(nil)

	var flow loginFlow
	var vendor string
	switch provider {
	case "anthropic":
		flow = oauth.NewAnthropicLoginFlow(store, httpc)
		vendor = "Claude / Anthropic"
	case "openai":
		flow = oauth.NewOpenAILoginFlow(store, httpc)
		vendor = "ChatGPT / OpenAI"
	}

	fmt.Printf("WARNING: Using a %s subscription from a non-official client is\n", vendor)
	fmt.Println("undocumented and may violate the provider's terms of service. Proceed at")
	fmt.Println("your own risk.")
	fmt.Println()
	fmt.Println("1. Open this URL in your browser and approve access:")
	fmt.Println()
	fmt.Println("   " + flow.AuthorizeURL())
	fmt.Println()
	fmt.Print("2. Paste the authorization code shown after approval: ")

	reader := bufio.NewReader(os.Stdin)
	code, err := reader.ReadString('\n')
	if err != nil {
		fmt.Fprintf(os.Stderr, "read code: %v\n", err)
		os.Exit(1)
	}
	code = strings.TrimSpace(code)
	if code == "" {
		fmt.Fprintln(os.Stderr, "no code provided")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := flow.Complete(ctx, code); err != nil {
		fmt.Fprintf(os.Stderr, "login failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Logged in. Credentials saved to " + store.Path)
}

// runLogout removes stored subscription credentials for a provider.
func runLogout(args []string) {
	provider, err := parseLoginArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	store := &oauth.Store{Path: oauth.DefaultCredentialsPath()}
	if err := store.Delete(provider); err != nil {
		fmt.Fprintf(os.Stderr, "logout failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Logged out of " + provider + " subscription.")
}

// runLoginStatus prints subscription login status.
func runLoginStatus(args []string) {
	provider, err := parseLoginArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	store := &oauth.Store{Path: oauth.DefaultCredentialsPath()}
	fmt.Printf("%s: %s\n", provider, oauth.Status(store, provider, time.Now()))
}

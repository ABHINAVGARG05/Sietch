package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/substantialcattle5/sietch/internal/config"
	"github.com/substantialcattle5/sietch/internal/constants"
	"github.com/substantialcattle5/sietch/internal/encryption/keys"
	"github.com/substantialcattle5/sietch/internal/fs"
	"github.com/substantialcattle5/sietch/internal/manifest"
	"github.com/substantialcattle5/sietch/internal/scaffold"
	"github.com/substantialcattle5/sietch/internal/validation"
	"github.com/substantialcattle5/sietch/internal/vault"
)

// TODO
// 1. creates vault based on templates
// 2. user can save their own templates which will be stored in .config/sietch/templates
// 3. user can edit templates but they have to go to .config/sietch/templates and edit the yaml file
// 4. user can recover standard templates by resetting .config/sietch/templates
//

func runScaffold(templateName, name, path string, force bool) error {
	// Ensure config directories exist
	if err := scaffold.EnsureConfigDirectories(); err != nil {
		return fmt.Errorf("failed to ensure config directories: %v", err)
	}

	// Ensure default templates are available
	if err := scaffold.EnsureDefaultTemplates(); err != nil {
		return fmt.Errorf("failed to ensure default templates: %v", err)
	}

	// Load and validate the template
	template, err := scaffold.ValidateTemplate(templateName)
	if err != nil {
		return fmt.Errorf("failed to validate template: %v", err)
	}

	fmt.Printf("Loading template: %s\n", template.Name)
	fmt.Printf("Description: %s\n", template.Description)

	// Use template name as vault name if not provided
	if name == "" {
		name = template.Name
	}

	// Use current directory if path not provided
	if path == "" {
		path = "."
	}

	// Prepare vault path and check for existing vault
	absVaultPath, err := vault.PrepareVaultPath(path, name, force)
	if err != nil {
		return err
	}

	// Create basic vault structure
	if err := fs.CreateVaultStructure(absVaultPath); err != nil {
		return fmt.Errorf("failed to create vault structure: %w", err)
	}

	// Create template-specific directories
	for _, dir := range template.Directories {
		dirPath := filepath.Join(absVaultPath, dir)
		if err := fs.EnsureDirectory(dirPath); err != nil {
			return fmt.Errorf("failed to create template directory %s: %v", dir, err)
		}
		fmt.Printf("Created directory: %s\n", dir)
	}

	// Create template-specific files
	for _, file := range template.Files {
		filePath := filepath.Join(absVaultPath, file.Path)

		// Ensure parent directory exists
		parentDir := filepath.Dir(filePath)
		if err := fs.EnsureDirectory(parentDir); err != nil {
			return fmt.Errorf("failed to create parent directory for %s: %v", file.Path, err)
		}

		// Parse file mode
		mode := os.FileMode(0644) // default
		if file.Mode != "" {
			if parsedMode, err := strconv.ParseUint(file.Mode, 8, 32); err == nil {
				mode = os.FileMode(parsedMode)
			}
		}

		// Write file content
		if err := os.WriteFile(filePath, []byte(file.Content), mode); err != nil {
			return fmt.Errorf("failed to create template file %s: %v", file.Path, err)
		}
		fmt.Printf("Created file: %s\n", file.Path)
	}

	// Generate encryption key using AES (default for templates)
	keyParams := validation.KeyGenParams{
		KeyType:          constants.EncryptionTypeAES,
		UsePassphrase:    false, // Default no passphrase for scaffolded vaults
		KeyFile:          "",
		AESMode:          constants.AESModeGCM,
		UseScrypt:        true,
		ScryptN:          constants.DefaultScryptN,
		ScryptR:          constants.DefaultScryptR,
		ScryptP:          constants.DefaultScryptP,
		PBKDF2Iterations: constants.DefaultPBKDF2Iters,
	}

	keyConfig, err := validation.HandleKeyGeneration(nil, absVaultPath, keyParams)
	if err != nil {
		scaffoldCleanupOnError(absVaultPath)
		return fmt.Errorf("key generation failed: %w", err)
	}

	// Generate vault ID
	vaultID := uuid.New().String()

	// Create the key path for storing the key file
	keyPath := filepath.Join(absVaultPath, ".sietch", "keys", "secret.key")

	// Write the key to file
	if keyConfig != nil && keyConfig.AESConfig != nil && keyConfig.AESConfig.Key != "" {
		// Key is already written by HandleKeyGeneration, just inform user
		fmt.Printf("Encryption key stored at: %s\n", keyPath)
	}

	// Build vault configuration using template settings
	cfg := &template.Config
	configuration := config.BuildVaultConfigWithDeduplication(
		vaultID,
		name,
		"", // Author will be prompted or use default
		constants.EncryptionTypeAES,
		keyPath,
		false, // No passphrase protection for scaffolded vaults
		cfg.ChunkingStrategy,
		cfg.ChunkSize,
		cfg.HashAlgorithm,
		cfg.Compression,
		cfg.SyncMode,
		template.Tags, // Use template tags
		keyConfig,
		// Deduplication parameters from template
		cfg.EnableDedup,
		cfg.DedupStrategy,
		cfg.DedupMinSize,
		cfg.DedupMaxSize,
		cfg.DedupGCThreshold,
		cfg.DedupIndexEnabled,
		cfg.DedupCrossFile,
	)

	// Initialize RSA config if not present
	if configuration.Sync.RSA == nil {
		configuration.Sync.RSA = &config.RSAConfig{
			KeySize:      constants.DefaultRSAKeySize,
			TrustedPeers: []config.TrustedPeer{},
		}
	}

	// Generate RSA key pair for sync
	err = keys.GenerateRSAKeyPair(absVaultPath, &configuration)
	if err != nil {
		scaffoldCleanupOnError(absVaultPath)
		return fmt.Errorf("failed to generate RSA keys for sync: %w", err)
	}

	// Write configuration to manifest
	if err := manifest.WriteManifest(absVaultPath, configuration); err != nil {
		scaffoldCleanupOnError(absVaultPath)
		return fmt.Errorf("failed to write vault manifest: %w", err)
	}

	// Print success message
	fmt.Printf("\n✅ Successfully scaffolded '%s' vault at: %s\n", template.Name, absVaultPath)
	fmt.Printf("📝 Template: %s (v%s)\n", template.Name, template.Version)
	fmt.Printf("🔐 Encryption: AES-256-GCM\n")
	fmt.Printf("📦 Chunking: %s (%s chunks)\n", cfg.ChunkingStrategy, cfg.ChunkSize)
	if cfg.EnableDedup {
		fmt.Printf("♻️  Deduplication: Enabled (%s strategy)\n", cfg.DedupStrategy)
	}
	fmt.Printf("🗜️  Compression: %s\n", cfg.Compression)
	fmt.Printf("\nYour vault is ready to use! Add files with: sietch add <files>\n")

	return nil
}

func scaffoldCleanupOnError(absVaultPath string) {
	// Attempt to clean up partially created vault on error
	_ = os.RemoveAll(absVaultPath)
}

var scaffoldCmd = &cobra.Command{
	Use:   "scaffold",
	Short: "Scaffold a new Sietch vault",
	Long: `Scaffold a new Sietch vault with secure encryption and configurable options.
	This creates the necessary directory structure and configuration files for your vault.
	
	Examples:
		sietch scaffold --template photoVault
		sietch scaffold --template photoVault --name "My Photo Vault"
		sietch scaffold --template photoVault --name "My Photo Vault" --path /path/to/vault
		sietch scaffold --template photoVault --name "My Photo Vault" --path /path/to/vault --force`,

	RunE: func(cmd *cobra.Command, args []string) error {
		// Check if user wants to list templates
		list, _ := cmd.Flags().GetBool("list")
		if list {
			return scaffold.ListTemplates()
		}

		// Get flag values
		template, _ := cmd.Flags().GetString("template")
		if template == "" {
			return fmt.Errorf("template is required. Use --list to see available templates")
		}

		name, _ := cmd.Flags().GetString("name")
		path, _ := cmd.Flags().GetString("path")
		force, _ := cmd.Flags().GetBool("force")

		return runScaffold(template, name, path, force)
	},
}

func init() {
	rootCmd.AddCommand(scaffoldCmd)

	// Add required flags
	scaffoldCmd.Flags().StringP("template", "t", "", "Template to use for scaffolding (required)")
	scaffoldCmd.Flags().StringP("name", "n", "", "Name for the vault (optional)")
	scaffoldCmd.Flags().StringP("path", "p", "", "Path where to create the vault (optional)")
	scaffoldCmd.Flags().BoolP("force", "f", false, "Force creation even if directory exists")
	scaffoldCmd.Flags().BoolP("list", "l", false, "List available templates")

	// Template is conditionally required (not when listing)
}

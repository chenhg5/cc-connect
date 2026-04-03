# Homebrew Installation

## Quick Start

```bash
# Add the tap
brew tap chenhg5/cc-connect

# Install cc-connect
brew install cc-connect

# Start as a background service
brew services start cc-connect
```

## Manual Installation (without tap)

You can also install directly from the formula file:

```bash
# Download and install directly
brew install https://raw.githubusercontent.com/chenhg5/cc-connect/main/homebrew/cc-connect.rb
```

## Configuration

After installation, create a config file:

```bash
# Create config directory
mkdir -p ~/.cc-connect

# Run setup wizard
cc-connect setup

# Or create config manually
cat > ~/.cc-connect/config.toml << 'EOF'
[[projects]]
name = "my-project"

[projects.agent]
type = "claudecode"

[projects.agent.options]
work_dir = "~/workspace"

[[projects.platforms]]
type = "telegram"

[projects.platforms.options]
token = "YOUR_BOT_TOKEN"
EOF
```

## Brew Services

Start cc-connect as a background service:

```bash
# Start the service
brew services start cc-connect

# Check status
brew services list

# View logs
tail -f $(brew --prefix)/var/log/cc-connect.log

# Stop the service
brew services stop cc-connect

# Restart the service
brew services restart cc-connect
```

## Upgrading

```bash
brew upgrade cc-connect
```

## Uninstalling

```bash
brew uninstall cc-connect
brew untap chenhg5/cc-connect
```

## For Maintainers

### Updating the Formula

The formula is automatically updated when a new release is published via the `homebrew.yml` workflow.

To manually update:

1. Update the `version` field in `cc-connect.rb`
2. Download the release assets and calculate SHA256:
   ```bash
   VERSION="v1.2.3"
   sha256sum cc-connect-${VERSION}-darwin-arm64.tar.gz
   ```
3. Update the `sha256` fields in the formula
4. Commit and push

### Creating the Homebrew Tap Repository

1. Create a new repository: `homebrew-cc-connect`
2. Copy `cc-connect.rb` to the root of that repository
3. Users can then install with:
   ```bash
   brew tap chenhg5/cc-connect
   brew install cc-connect
   ```

### Testing the Formula Locally

```bash
# Build from source (for testing)
brew install --build-from-source ./homebrew/cc-connect.rb

# Audit the formula
brew audit ./homebrew/cc-connect.rb

# Test the formula
brew test cc-connect
```
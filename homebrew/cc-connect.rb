# frozen_string_literal: true

class CcConnect < Formula
  desc "Bridge AI coding agents to any messaging platform"
  homepage "https://github.com/chenhg5/cc-connect"
  version "v1.2.2-beta.5"
  license "MIT"

  on_macos do
    on_intel do
      url "https://github.com/chenhg5/cc-connect/releases/download/#{version}/cc-connect-#{version}-darwin-amd64.tar.gz"
      sha256 "PLACEHOLDER_DARWIN_AMD64_SHA256"
    end
    on_arm do
      url "https://github.com/chenhg5/cc-connect/releases/download/#{version}/cc-connect-#{version}-darwin-arm64.tar.gz"
      sha256 "PLACEHOLDER_DARWIN_ARM64_SHA256"
    end
  end

  on_linux do
    on_intel do
      url "https://github.com/chenhg5/cc-connect/releases/download/#{version}/cc-connect-#{version}-linux-amd64.tar.gz"
      sha256 "PLACEHOLDER_LINUX_AMD64_SHA256"
    end
    on_arm do
      url "https://github.com/chenhg5/cc-connect/releases/download/#{version}/cc-connect-#{version}-linux-arm64.tar.gz"
      sha256 "PLACEHOLDER_LINUX_ARM64_SHA256"
    end
  end

  def install
    bin.install "cc-connect"

    # Create example config directory
    (etc/"cc-connect").mkpath

    # Generate default config path
    ohai "Config file will be created at ~/.cc-connect/config.toml on first run"

    # Install completion scripts if available
    generate_completions_from_executable(bin/"cc-connect", "completion", shells: [:bash, :zsh, :fish]) if File.exist?(bin/"cc-connect")
  end

  def caveats
    <<~EOS
      cc-connect requires a config file at ~/.cc-connect/config.toml

      Quick start:
        1. Run: cc-connect setup
        2. Or create config manually:
           mkdir -p ~/.cc-connect
           cp #{opt_libexec}/config.example.toml ~/.cc-connect/config.toml

      For brew services:
        brew services start cc-connect

      Documentation:
        https://github.com/chenhg5/cc-connect#readme
    EOS
  end

  service do
    run [opt_bin/"cc-connect", "--config", "~/.cc-connect/config.toml"]
    keep_alive true
    log_path var/"log/cc-connect.log"
    error_log_path var/"log/cc-connect.error.log"
    working_dir Dir.home
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/cc-connect version")
  end
end
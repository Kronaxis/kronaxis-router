# Homebrew formula for kronaxis-router
# This is a template; goreleaser generates the actual formula in the tap repo.
# Manual install: brew install kronaxis/tap/kronaxis-router

class KronaxisRouter < Formula
  desc "Intelligent LLM proxy with cost-optimised routing"
  homepage "https://github.com/Kronaxis/kronaxis-router"
  license "Apache-2.0"
  version "1.0.0"

  on_macos do
    on_arm do
      url "https://github.com/Kronaxis/kronaxis-router/releases/download/v#{version}/kronaxis-router_#{version}_darwin_arm64.tar.gz"
      # sha256 will be filled by goreleaser
    end
    on_intel do
      url "https://github.com/Kronaxis/kronaxis-router/releases/download/v#{version}/kronaxis-router_#{version}_darwin_amd64.tar.gz"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/Kronaxis/kronaxis-router/releases/download/v#{version}/kronaxis-router_#{version}_linux_arm64.tar.gz"
    end
    on_intel do
      url "https://github.com/Kronaxis/kronaxis-router/releases/download/v#{version}/kronaxis-router_#{version}_linux_amd64.tar.gz"
    end
  end

  def install
    bin.install "kronaxis-router"
    etc.install "config.yaml" => "kronaxis-router/config.yaml"
  end

  def post_install
    ohai "Run 'kronaxis-router init' to auto-detect backends and generate config"
    ohai "Run 'kronaxis-router' to start the router"
    ohai "Dashboard at http://localhost:8050"
  end

  service do
    run [opt_bin/"kronaxis-router"]
    keep_alive true
    working_dir var/"kronaxis-router"
    log_path var/"log/kronaxis-router.log"
    error_log_path var/"log/kronaxis-router.log"
    environment_variables CONFIG_PATH: etc/"kronaxis-router/config.yaml"
  end

  test do
    assert_match "kronaxis-router v", shell_output("#{bin}/kronaxis-router version")
  end
end

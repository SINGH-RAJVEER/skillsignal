{ pkgs, lib, config, ... }:

let
	# Ports follow docker-compose defaults. Override per invocation when they
	# collide with other local services, e.g.:
	#   SKILLSIGNAL_API_PORT=8010 SKILLSIGNAL_WEB_PORT=3010 devenv up
	apiPort =
		let value = builtins.getEnv "SKILLSIGNAL_API_PORT";
		in if value == "" then "8000" else value;
	webPort =
		let value = builtins.getEnv "SKILLSIGNAL_WEB_PORT";
		in if value == "" then "3000" else value;

	# DATABASE_URL is assembled at process start from PGHOST/PGPORT because
	# devenv resolves the postgres port per invocation when 5432 is busy.
	processDbUrl =
		"export DATABASE_URL=\"postgresql://postgres:password@\$PGHOST:\$PGPORT/skillsignal\"";

	# Compiled Go binaries live in devenv state, outside the source tree.
	binDir = "${config.devenv.root}/.devenv/state/go";
in
{
	dotenv.disableHint = true;

	languages.javascript = {
		enable = true;
		bun.enable = true;
		bun.install.enable = true;
	};

	# The API and worker are Go modules joined by go.work at the repo root.
	languages.go.enable = true;

	scripts.go-build.exec = ''
		go build ./apps/api/cmd/api ./apps/worker/cmd/worker
	'';
	scripts.go-test.exec = ''
		CGO_ENABLED=0 go test ./libs/go-core/... ./apps/api/... ./apps/worker/...
	'';
	scripts.go-migrate.exec = ''
		go run ./apps/api/cmd/api migrate
	'';

	services.postgres = {
		enable = true;
		package = pkgs.postgresql_18;
		listen_addresses = "127.0.0.1";
		initialDatabases = [
			{
				name = "skillsignal";
				user = "postgres";
				pass = "password";
			}
		];
	};

	env = {
		JWT_SECRET = "devenv-local-jwt-secret-not-for-production";
		STORAGE_ROOT = "${config.devenv.root}/.local-storage";
		WEB_URL = "http://localhost:${webPort}";
		VITE_API_URL = "http://localhost:${apiPort}";
		# Nothing in the Go tree needs cgo, and NixOS has no default gcc on PATH.
		CGO_ENABLED = "0";
	};

	processes.api = {
		# The binary is built and exec'd rather than run through `go run`,
		# which forks the compiled binary as a child and leaves it orphaned
		# holding the port when process-compose stops the wrapper.
		exec = lib.concatStringsSep "\n" [
			processDbUrl
			"export PORT=${apiPort}"
			"mkdir -p ${binDir}"
			"go build -o ${binDir}/api ./cmd/api"
			# Same boot sequence as apps/api/Dockerfile: migrate, then serve.
			"${binDir}/api migrate && exec ${binDir}/api serve"
		];
		cwd = "${config.devenv.root}/apps/api";
		after = [ "devenv:processes:postgres@ready" ];
		# The first run compiles the module; allow a few minutes of boot time.
		ready.http.get.port = lib.toInt apiPort;
		ready.http.get.path = "/healthz";
		ready.period = 3;
		ready.probe_timeout = 2;
		ready.failure_threshold = 60;
	};

	processes.worker = {
		exec = lib.concatStringsSep "\n" [
			processDbUrl
			"mkdir -p ${binDir}"
			"go build -o ${binDir}/worker ./cmd/worker"
			"exec ${binDir}/worker"
		];
		cwd = "${config.devenv.root}/apps/worker";
		# Wait for the API so migrations have created the job tables.
		after = [ "devenv:processes:api@ready" ];
	};

	processes.web = {
		# vite is exec'd directly rather than via `bun run dev`: the bun wrapper
		# forks vite as a child, so a stop signal leaves it holding the port and
		# every restart then fails with "Port ${webPort} is already in use".
		# vite binds [::1] only, and localhost resolves to ::1 here, so the
		# readiness probe below must target [::1] rather than the IPv4 default.
		exec = "exec node_modules/.bin/vite --strictPort --port ${webPort}";
		cwd = "${config.devenv.root}/apps/web";
		ready.http.get.host = "[::1]";
		ready.http.get.port = lib.toInt webPort;
		ready.http.get.path = "/";
	};

	enterShell = ''
		export DATABASE_URL="postgresql://postgres:password@$PGHOST:$PGPORT/skillsignal"

		# Local secrets (.env, gitignored) feed optional integrations such as
		# OpenRouter and Razorpay. Existing variables win so the preview keeps
		# targeting the devenv-managed Postgres.
		if [ -f "$DEVENV_ROOT/.env" ]; then
			while IFS='=' read -r key value || [ -n "$key" ]; do
				case "$key" in ""|"#"*) continue ;; esac
				if [ -z "''${!key+x}" ]; then
					export "''${key}=''${value%$'\r'}"
				fi
			done < "$DEVENV_ROOT/.env"
		fi
	'';

	infoSections."developer preview" = [
		"web      http://localhost:${webPort}"
		"api      http://localhost:${apiPort}/healthz"
		"postgres psql --dbname skillsignal --username postgres (uses $PGHOST/$PGPORT)"
	];
}

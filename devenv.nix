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
	servicePorts = {
		identity = "8101";
		hiring = "8102";
		documents = "8103";
		evaluation = "8104";
		billing = "8105";
	};

	# DATABASE_URL is assembled at process start from PGHOST/PGPORT because
	# devenv resolves the postgres port per invocation when 5432 is busy.
	processDbUrl =
		"export DATABASE_URL=\"postgresql://postgres:password@\$PGHOST:\$PGPORT/skillsignal\"";

	# Compiled Go binaries live in devenv state, outside the source tree.
	binDir = "${config.devenv.root}/.devenv/state/go";
	mkDomainProcess = name: port: dependencies: {
		exec = lib.concatStringsSep "\n" [
			processDbUrl
			"export PORT=${port}"
			"mkdir -p ${binDir}"
			"go build -o ${binDir}/${name} ./services/cmd/${name}"
			"exec ${binDir}/${name}"
		];
		cwd = config.devenv.root;
		after = dependencies;
		ready.http.get.port = lib.toInt port;
		ready.http.get.path = "/readyz";
		ready.period = 3;
		ready.probe_timeout = 2;
		ready.failure_threshold = 60;
	};
in
{
	dotenv.disableHint = true;

	languages.javascript = {
		enable = true;
		bun.enable = true;
		bun.install.enable = true;
	};

	# The domain processes, gateway, workers, and shared libraries use go.work.
	languages.go.enable = true;

	scripts.go-build.exec = ''
		go build ./apps/api/cmd/api ./apps/gateway/cmd/gateway ./apps/worker/cmd/worker ./services/cmd/...
	'';
	scripts.go-test.exec = ''
		CGO_ENABLED=0 go test ./libs/contracts/... ./libs/go-core/... ./apps/api/... ./apps/gateway/... ./apps/worker/... ./services/...
	'';
	scripts.go-migrate.exec = ''
		go run ./apps/api/cmd/api
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

	processes.identity = {
		exec = lib.concatStringsSep "\n" [
			processDbUrl
			"export PORT=${servicePorts.identity}"
			"mkdir -p ${binDir}"
			"go build -o ${binDir}/migrate ./apps/api/cmd/api"
			"${binDir}/migrate"
			"go build -o ${binDir}/identity ./services/cmd/identity"
			"exec ${binDir}/identity"
		];
		cwd = config.devenv.root;
		after = [ "devenv:processes:postgres@ready" ];
		ready.http.get.port = lib.toInt servicePorts.identity;
		ready.http.get.path = "/readyz";
		ready.period = 3;
		ready.probe_timeout = 2;
		ready.failure_threshold = 60;
	};

	processes.hiring = mkDomainProcess "hiring" servicePorts.hiring [ "devenv:processes:identity@ready" ];
	processes.documents = mkDomainProcess "documents" servicePorts.documents [ "devenv:processes:identity@ready" ];
	processes.evaluation = mkDomainProcess "evaluation" servicePorts.evaluation [ "devenv:processes:identity@ready" ];
	processes.billing = mkDomainProcess "billing" servicePorts.billing [ "devenv:processes:identity@ready" ];

	processes.gateway = {
		exec = lib.concatStringsSep "\n" [
			"export PORT=${apiPort}"
			"export IDENTITY_URL=http://127.0.0.1:${servicePorts.identity}"
			"export HIRING_URL=http://127.0.0.1:${servicePorts.hiring}"
			"export DOCUMENTS_URL=http://127.0.0.1:${servicePorts.documents}"
			"export EVALUATION_URL=http://127.0.0.1:${servicePorts.evaluation}"
			"export BILLING_URL=http://127.0.0.1:${servicePorts.billing}"
			"mkdir -p ${binDir}"
			"go build -o ${binDir}/gateway ./apps/gateway/cmd/gateway"
			"exec ${binDir}/gateway"
		];
		cwd = config.devenv.root;
		after = [
			"devenv:processes:identity@ready"
			"devenv:processes:hiring@ready"
			"devenv:processes:documents@ready"
			"devenv:processes:evaluation@ready"
			"devenv:processes:billing@ready"
		];
		ready.http.get.port = lib.toInt apiPort;
		ready.http.get.path = "/readyz";
		ready.period = 3;
		ready.probe_timeout = 2;
		ready.failure_threshold = 60;
	};

	processes.parser-worker = {
		exec = lib.concatStringsSep "\n" [
			processDbUrl
			"export WORKER_ROLE=parser"
			"unset OPENROUTER_API_KEY"
			"mkdir -p ${binDir}"
			"go build -o ${binDir}/parser-worker ./apps/worker/cmd/worker"
			"exec ${binDir}/parser-worker"
		];
		cwd = config.devenv.root;
		after = [ "devenv:processes:identity@ready" ];
	};

	processes.inference-worker = {
		exec = lib.concatStringsSep "\n" [
			processDbUrl
			"export WORKER_ROLE=inference"
			"mkdir -p ${binDir}"
			"go build -o ${binDir}/inference-worker ./apps/worker/cmd/worker"
			"exec ${binDir}/inference-worker"
		];
		cwd = config.devenv.root;
		after = [ "devenv:processes:identity@ready" ];
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
		"gateway  http://localhost:${apiPort}/readyz"
		"postgres psql --dbname skillsignal --username postgres (uses $PGHOST/$PGPORT)"
	];
}

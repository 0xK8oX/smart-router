module.exports = {
  apps: [
    {
      name: "smart-router",
      script: "./start.sh",
      cwd: "/Volumes/Proj/workspace/smart-router",
      interpreter: "bash",
      exec_mode: "fork",
      instances: 1,
      autorestart: true,
      watch: false,
      max_memory_restart: "512M",
      min_uptime: "10s",
      max_restarts: 10,
      env: {
        NODE_ENV: "production",
      },
      log_file: "/tmp/smart-router-pm2.log",
      error_file: "/tmp/smart-router-err.log",
      out_file: "/tmp/smart-router-out.log",
      merge_logs: true,
      // PM2 sends SIGKILL 5s after SIGTERM; streaming requests can run 60s+.
      // This must exceed Go's srv.Shutdown timeout by a margin.
      kill_timeout: 135000,
      listen_timeout: 10000,
      // Don't auto-restart on failure more than 3 times — we want to know.
      max_restarts: 3,
      restart_delay: 3000,
    },
  ],
};

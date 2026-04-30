module.exports = {
  apps: [
    {
      name: "smart-router",
      script: "./node_modules/.bin/wrangler",
      args: "dev --port 8790",
      cwd: "/Volumes/Proj/workspace/smart-router",
      exec_mode: "fork",
      instances: 1,
      autorestart: true,
      watch: false,
      max_memory_restart: "512M",
      min_uptime: "10s",
      max_restarts: 10,
      env: {
        NODE_ENV: "development",
      },
      log_file: "/tmp/smart-router-pm2.log",
      error_file: "/tmp/smart-router-err.log",
      out_file: "/tmp/smart-router-out.log",
      merge_logs: true,
      kill_timeout: 5000,
      listen_timeout: 10000,
      // Health check - detects zombie states where workerd crashes but wrangler doesn't exit
      health_check: "http://localhost:8790/v1/health",
      health_check_interval: 30000,
      health_check_timeout: 5000,
      health_check_max_fails: 2,
      // Force restart every hour as extra safety net
      cron_restart: "0 * * * *",
    },
  ],
};

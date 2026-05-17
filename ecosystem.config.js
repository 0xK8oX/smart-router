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
      kill_timeout: 5000,
      listen_timeout: 10000,
    },
  ],
};

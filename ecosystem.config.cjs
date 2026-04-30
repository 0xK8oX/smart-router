module.exports = {
  apps: [
    {
      name: "smart-router",
      script: "./start.sh",
      cwd: "/Volumes/Proj/workspace/smart-router",
      interpreter: "bash",
      env: {
        NODE_ENV: "development",
      },
      log_file: "/tmp/smart-router-pm2.log",
      out_file: "/tmp/smart-router-out.log",
      error_file: "/tmp/smart-router-err.log",
      merge_logs: true,
      log_date_format: "YYYY-MM-DD HH:mm:ss Z",
      autorestart: true,
      restart_delay: 3000,
      max_restarts: 10,
      min_uptime: "10s",
      watch: false,
      exec_mode: "fork",
    },
  ],
};

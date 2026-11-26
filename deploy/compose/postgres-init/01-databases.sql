-- One database and one role per service. The role owns only its own database,
-- so the boundary Article IX promises exists on the first day, not on the day
-- someone remembers to add it. These passwords are local development only.
CREATE ROLE echo_auth_app         LOGIN PASSWORD 'echo_auth';
CREATE ROLE echo_wallet_app       LOGIN PASSWORD 'echo_wallet';
CREATE ROLE echo_transfer_app     LOGIN PASSWORD 'echo_transfer';
CREATE ROLE echo_notification_app LOGIN PASSWORD 'echo_notification';
CREATE ROLE echo_webhook_app      LOGIN PASSWORD 'echo_webhook';
CREATE ROLE echo_recon_app        LOGIN PASSWORD 'echo_recon';

CREATE DATABASE echo_auth         OWNER echo_auth_app;
CREATE DATABASE echo_wallet       OWNER echo_wallet_app;
CREATE DATABASE echo_transfer     OWNER echo_transfer_app;
CREATE DATABASE echo_notification OWNER echo_notification_app;
CREATE DATABASE echo_webhook      OWNER echo_webhook_app;
CREATE DATABASE echo_recon        OWNER echo_recon_app;

-- Create listmonk user and database for self-hosted newsletter service
-- Listmonk handles its own table migrations upon startup

CREATE USER listmonk WITH PASSWORD 'listmonk_dev';
CREATE DATABASE listmonk OWNER listmonk;

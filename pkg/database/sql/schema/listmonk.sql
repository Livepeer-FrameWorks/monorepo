-- ============================================================================
-- LISTMONK DATABASE INITIALIZATION
-- Creates the listmonk database for the self-hosted newsletter service.
-- Listmonk handles its own table migrations on container startup
-- (entrypoint: `listmonk --install --idempotent --yes`); no schema DDL here.
--
-- Listmonk connects as the postgres admin user (matches the prod path in
-- cli/pkg/provisioner/listmonk_role.go where DATABASE_USER defaults to
-- 'postgres'); no separate application role is provisioned.
-- ============================================================================

CREATE DATABASE listmonk;

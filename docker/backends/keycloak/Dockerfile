# Keycloak with the three demo realms baked in.
#
# The development stack (docker/backends/docker-compose.yml) bind-mounts
# ./keycloak/realms into the stock Keycloak image, which is fine when you have
# the repo. The standalone events-stack.yaml has no sibling files to mount, so
# the realms have to travel inside an image instead.
#
# Build context is this directory:
#   docker build -t ghcr.io/panyam/mcpkit-events-keycloak:latest docker/backends/keycloak
#
# The realm JSONs carry demo-only client secrets. See README.md in this
# directory for the bring-your-own-client recipe before pointing anything real
# at this image.
FROM quay.io/keycloak/keycloak:26.0

COPY realms/ /opt/keycloak/data/import/

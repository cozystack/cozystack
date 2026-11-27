#!/bin/bash
# docker-entrypoint.sh of docker.io/alerta/alerta-web:9.1.0
# (sha256:693845fb8a95a483c8c9d560786f661f5b6198e5ea0baa6ddf368a7e53c94f39),
# Apache-2.0, mounted over the image's copy by templates/alerta/alerta.yaml.
# Changed from upstream only so that Alerta's credentials stay out of the
# container log: the environment dump is removed and the output of the two
# `alertad key` calls is discarded.
set -e

JINJA2="import os, sys, jinja2; sys.stdout.write(jinja2.Template(sys.stdin.read()).render(env=os.environ)+'\n')"

ALERTA_CONF_FILE=${ALERTA_CONF_FILE:-/app/alerta.conf}
ALERTA_SVR_CONF_FILE=${ALERTA_SVR_CONF_FILE:-/app/alertad.conf}
ALERTA_WEB_CONF_FILE=${ALERTA_WEB_CONF_FILE:-/web/config.json}
NGINX_CONF_FILE=/app/nginx.conf
UWSGI_CONF_FILE=/app/uwsgi.ini
SUPERVISORD_CONF_FILE=/app/supervisord.conf

ADMIN_USER=${ADMIN_USERS%%,*}
ADMIN_PASSWORD=${ADMIN_PASSWORD:-alerta}
MAXAGE=${ADMIN_KEY_MAXAGE:-315360000}  # default=10 years


# Generate minimal server config, if not supplied
if [ ! -f "${ALERTA_SVR_CONF_FILE}" ]; then
  echo "# Create server configuration file."
  export SECRET_KEY=${SECRET_KEY:-$(< /dev/urandom tr -dc A-Za-z0-9_\!\@\#\$\%\^\&\*\(\)-+= | head -c 32)}
  python3 -c "${JINJA2}" < ${ALERTA_SVR_CONF_FILE}.j2 >${ALERTA_SVR_CONF_FILE}
  unset SECRET_KEY
fi

# Init admin users and API keys
if [ -n "${ADMIN_USERS}" ]; then
  echo "# Create admin users."
  alertad user --all --password "${ADMIN_PASSWORD}" || true
  echo "# Create admin API keys."
  alertad key --all >/dev/null

  # Create user-defined API key, if required
  if [ -n "${ADMIN_KEY}" ]; then
    echo "# Create user-defined admin API key."
    alertad key --username "${ADMIN_USER}" --key "${ADMIN_KEY}" --duration "${MAXAGE}" >/dev/null || true
  fi
fi

# Generate minimal client config, if not supplied
if [ ! -f "${ALERTA_CONF_FILE}" ]; then
  # Add API key to client config, if required
  if [ "${AUTH_REQUIRED,,}" == "true" ]; then
    echo "# Auth enabled; add admin API key to client configuration."
    HOUSEKEEPING_SCOPES="--scope read --scope write:alerts --scope admin:management"
    if grep -qE 'CUSTOMER_VIEWS.*=.*True' ${ALERTA_SVR_CONF_FILE};then
      HOUSEKEEPING_SCOPES="--scope admin:alerts ${HOUSEKEEPING_SCOPES}"
    fi
    export API_KEY=$(alertad key \
    --username "${ADMIN_USER}" \
    ${HOUSEKEEPING_SCOPES} \
    --duration "${MAXAGE}" \
    --text "Housekeeping")
  fi
  echo "# Create client configuration file."
  python3 -c "${JINJA2}" < ${ALERTA_CONF_FILE}.j2 >${ALERTA_CONF_FILE}
fi

# Generate supervisord config, if not supplied
if [ ! -f "${SUPERVISORD_CONF_FILE}" ]; then
  echo "# Create supervisord configuration file."
  python3 -c "${JINJA2}" < ${SUPERVISORD_CONF_FILE}.j2 >${SUPERVISORD_CONF_FILE}
fi

# Generate nginx config, if not supplied.
if [ ! -f "${NGINX_CONF_FILE}" ]; then
  echo "# Create nginx configuration file."
  python3 -c "${JINJA2}" < ${NGINX_CONF_FILE}.j2 >${NGINX_CONF_FILE}
fi
nginx -t -c ${NGINX_CONF_FILE}

# Generate uWSGI config, if not supplied.
if [ ! -f "${UWSGI_CONF_FILE}" ]; then
  echo "# Create uWSGI configuration file."
  python3 -c "${JINJA2}" < ${UWSGI_CONF_FILE}.j2 >${UWSGI_CONF_FILE}
fi

# Generate web config, if not supplied.
if [ ! -f "${ALERTA_WEB_CONF_FILE}" ]; then
  echo "# Create web configuration file."
  python3 -c "${JINJA2}" < ${ALERTA_WEB_CONF_FILE}.j2 >${ALERTA_WEB_CONF_FILE}
fi

echo
echo '# Checking versions.'
echo Alerta Server ${SERVER_VERSION}
echo Alerta Client ${CLIENT_VERSION}
echo Alerta WebUI  ${WEBUI_VERSION}

nginx -v
echo uwsgi $(uwsgi --version)
mongosh --version
psql --version
python3 --version
/venv/bin/pip list

echo
echo 'Alerta init process complete; ready for start up.'
echo

exec "$@"

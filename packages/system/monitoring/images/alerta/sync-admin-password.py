"""Set the password of every Alerta admin user to ADMIN_PASSWORD.

`alertad user --all` only creates admin users that are missing, so an admin
user that already exists keeps the password it was created with, which on
installations that predate the dedicated Alerta password is Grafana's. The
entrypoint runs this right after it, on every start. It never prints the
password, and a failure stops the container rather than leaving the old
password in place.
"""
import os
import sys

from alerta.app import create_app

password = os.environ.get('ADMIN_PASSWORD')
if not password:
    sys.exit('ADMIN_PASSWORD is not set')

app = create_app()
# Imported after the app exists: importing the model first runs into a
# circular import through alerta.auth.
from alerta.models.user import User  # noqa: E402

with app.app_context():
    for login in app.config['ADMIN_USERS']:
        user = User.find_by_username(login)
        if user is not None and not user.verify_password(password):
            user.update(password=password)
            print(f'# Reset the password of admin user {login}.')

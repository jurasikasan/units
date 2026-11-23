FROM authelia/authelia:4.39.28

COPY configuration.yml /config/configuration.yml
COPY entrypoint.sh /gate-entrypoint.sh
COPY gate-user /usr/local/bin/gate-user

ENV X_AUTHELIA_CONFIG_FILTERS=template \
    AUTHELIA_SESSION_SECRET_FILE=/secrets/session \
    AUTHELIA_STORAGE_ENCRYPTION_KEY_FILE=/secrets/storage \
    AUTHELIA_IDENTITY_VALIDATION_RESET_PASSWORD_JWT_SECRET_FILE=/secrets/reset-jwt \
    AUTHELIA_IDENTITY_PROVIDERS_OIDC_HMAC_SECRET_FILE=/secrets/oidc-hmac

ENTRYPOINT ["/gate-entrypoint.sh"]

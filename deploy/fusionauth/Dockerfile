FROM fusionauth/fusionauth-app:latest

# Copy kickstart configuration
COPY kickstart.json /usr/local/fusionauth/kickstart/kickstart.json

# FusionAuth will automatically use kickstart if FUSIONAUTH_APP_KICKSTART_FILE is set
ENV FUSIONAUTH_APP_KICKSTART_FILE=/usr/local/fusionauth/kickstart/kickstart.json

EXPOSE 9011

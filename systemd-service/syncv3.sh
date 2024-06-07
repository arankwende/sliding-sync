#!/usr/env/bin bash

SYNCV3_SECRET=$(cat /opt/syncv3/.secret) SYNCV3_SERVER="https://matrix-client.matrix.org" SYNCV3_DB="user=syncv3 dbname=syncv3 sslmode=disable password='PASSWORD'" SYNCV3_BINDADDR=0.0.0.0:8008 /opt/syncv3/syncv3_linux_amd64 


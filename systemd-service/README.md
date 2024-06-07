# systemd service for Sliding Sync
This folder contains a service script to run sliding sync (until it's made an official part of Synapse) as a systemd service.

The following steps will help you set it up in linux, specially in ubuntu, you should have installed all the dependencias and modules first (as outlined on the repos main readme):

First, we will create an user that will run sliding sync, the default would be syncv3 but any user can be created although you will have to modify the .service file:
```
adduser syncv3
```
Next, we will download the systemd service example into the folder we created (we should have our .secret file in the same folder), /opt/syncv3


```
wget -O /opt/syncv3/syncv3.service https://raw.githubusercontent.com/arankwende/sliding-sync/main/systemd-service/syncv3.service
```

as well as the bash file
```
wget -O /opt/syncv3/syncv3.service https://raw.githubusercontent.com/arankwende/sliding-sync/main/systemd-service/syncv3.sh
```


We need to edit the environment variables that will be given at run time directly in the bashfile file and populate it as specified on the sliding sync docs:
```
nano /opt/syncv3/syncv3.sh

```

We now make the user we created (in this example ipmimqtt) owner of the syncv3 folder:
```
chown -R syncv3 /opt/syncv3/

```
and we make the folder executable:
```
chmod -R +x /opt/syncv3

```

Now we copy the systemd example script into the user systemd folder:
```
cp /opt/syncv3/syncv3.service /etc/systemd/system/
```




We can now test it:
First we reload the systemctl daemon
```
sudo systemctl daemon-reload
```
then we start the service:
```
sudo systemctl start syncv3.service

```

it should start and we can its status:
```
sudo systemctl status syncv3.service

```
or check if the process  is running:

```
ps -aux
```


Once everything is working, we can enable the service in order for it to work on reboot:

```
sudo systemctl enable syncv3.service

```



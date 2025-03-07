`git clone https://github.com/sagostin/synergymattersfax.git`
then
`cd synergymattersfax`
then
`sudo ./build.sh`
then
`nano .env`
then paste this in the .env
```
FTP_ROOT=./synergyfax_ftp

FAX_NUMBER=TEN_DIGIT_NUMBER_HERE

SEND_WEBHOOK_URL=http://YOUR_FAX_SERVER_URL:8080/fax/send
SEND_WEBHOOK_USERNAME=YOUR_USERNAME_HERE
SEND_WEBHOOK_PASSWORD=YOUR_PASSWORD_HERE
```
then Ctrl+X and save.

then do
`sudo docker compose up -d`

then open up your browser, and navigate to http://SERVER_IP:8081
and create admin user, write down credentials
go to users tab once logged in, and create user with
`username: sf`
`password: sfpass`
then make new user, and set their root directory to be: `/srv/sftpgo/synergyfax_ftp`
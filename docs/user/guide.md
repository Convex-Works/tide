# Using tide

For the people who hold meetings. To run a server, see the
[deployment guide](../operations/deployment.md). To connect a program, see
[Bots](bots.md).

## Start a meeting

On the dashboard, select **New**. A name and a link are optional, and tide suggests a
link you can change. **Create & join** starts now; **Create** keeps the room for later.
A room stays yours and its link stays the same, so you can use it again.

If the server has no sign-in, the browser that created a room owns it, and the room is
deleted 24 hours after its last use. Keep using the same browser.

## Send people a time

Give the room a start and end time when you create it, and tide downloads a calendar
file to send. Each room also has **Add to calendar…**. tide keeps no times and sends no
reminders.

## Let people in

Guests open the link and type a name. They need no account. If the lobby is on, which is
the default, they wait until you admit or deny them in the People panel. You can turn it
off in the room's settings.

## In a meeting

You can hide someone's video for yourself from the menu on their tile. Their audio and
screen share stay, and nobody else is affected.

Chat lasts as long as you are in the meeting. Nobody can read earlier messages after
they join or reload.

If the server restarts, the meeting ends and the page offers **Rejoin**. Guests go through
the lobby again.

## Run a meeting

A room's owner, and an administrator, can mute or remove anyone from the menu on their
tile. A removed person is disconnected and has to ask to rejoin through the lobby. With the
lobby off, they can rejoin at once. Leaving offers **End meeting for all**.

## Record

Where the server records, hosts can record audio, or the meeting as video. Everyone sees
a red line at the top of the meeting while it runs. Recording continues if you close your
tab while others stay, and it stops when you stop it or the room empties.

Recordings are on the room's page, where you can download or delete them. Deleting a
recording deletes its transcript too.

## Get a transcript

Where the server allows it, a room's owner can have recordings transcribed on their own
Mac. The recording and the transcript never go to anyone else's computer, and no AI runs
on the server.

1. Open **Machines** and follow the steps: get the moil app, pair it, and approve the
   transcribe job it shows.
2. Record as usual. Meetings that end after you pair a machine are transcribed on their
   own, when the machine is awake. Older recordings have a button to ask for one.
3. Download the transcript as text or captions from the room's page.

The first transcript downloads 2.9 GB. moil is an alpha, and the job runs with your
permissions.

Speakers get their display names when tide is sure enough, and `Speaker 1` when not. A
voice on someone else's microphone gets that person's name, and a guest can type any
name, so don't treat a name as proof of who spoke.

## Report a problem

If you can't see or hear someone, run `tideDiagnostics()` in the browser console before
you reload, and send the file it saves to whoever runs your server. It stays on your
computer until you do, and it contains the names of people in the meeting.

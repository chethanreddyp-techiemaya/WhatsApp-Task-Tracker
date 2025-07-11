@echo off
echo Stopping WhatsApp bot if running...
taskkill /f /im main.exe 2>nul
taskkill /f /im whatsapp-bot.exe 2>nul

echo Backing up current session...
if _, err := os.Stat("session.db"); err == nil {
    // Try to backup and remove corrupted session
    fmt.Println("Backing up existing session.db...")
    os.Rename("session.db", "session.db.backup")
}

echo Session reset complete!
echo Run the bot again to scan QR code.
pause 

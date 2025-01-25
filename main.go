package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

var tokenFile = flag.String("token", ".token", "Telegram token.")

func main() {
	flag.Parse()

	botToken, err := filepath.Abs(*tokenFile)
	fatalOnErr(err)

	t, err := os.ReadFile(botToken)
	fatalOnErr(err)

	bot, err := telego.NewBot(strings.TrimSpace(string(t)), telego.WithDefaultDebugLogger())
	fatalOnErr(err)

	botUser, err := bot.GetMe()
	fatalOnErr(err)
	fmt.Printf("Bot User: %+v\n", botUser)

	updates, err := bot.UpdatesViaLongPolling(nil)
	fatalOnErr(err)
	defer bot.StopLongPolling()

	db, err := newDb("app.db")
	fatalOnErr(err)
	defer func() {
		_ = db.Close()
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	stopChan := make(chan struct{})
	go sendStatsAndDeleteBucketEveryNoon(bot, db, stopChan)

	err = db.GiveUserRoles(daunitseID, adminRole)
	if err != nil {
		log.Printf("Не смог назначить пользователю роль : %s", err)
	}
	err = db.GiveUserRoles(carpawellID, adminRole)
	if err != nil {
		log.Printf("Не смог назначить пользователю роль : %s", err)
	}

	for {
		select {
		case sig := <-sigChan:
			log.Printf("got signal: %s", sig)
			close(stopChan)

			return
		case upd := <-updates:
			handleUpdate(upd, db, bot)
		}
	}
}

func fatalOnErr(err error) {
	if err != nil {
		log.Printf("fatal error: %s", err)
		os.Exit(1)
	}
}

func handleUpdate(upd telego.Update, db *database, bot *telego.Bot) {
	if upd.Message == nil {
		log.Printf("got empty #%d update, skip\n", upd.UpdateID)
		return
	}

	userID := upd.Message.From.ID

	chatID := upd.Message.Chat.ID

	m := strings.ToLower(upd.Message.Text)

	role, err := db.UserRole(userID)
	if err != nil {
		log.Printf("Не смог узнать/назначить роль %d пользователя(ю) : %s", userID, err)
		return
	}

	groupID, err := db.ReadChatID()
	if err != nil {
		log.Printf("Не смог прочитать group id из бакета %s :%s", chatIDBucket, err)
	}

	err = increaseMessagesCounter(bot, db, chatID, groupID, userID)
	if err != nil {
		return
	}

	err = nonAutomaticChatIDChange(bot, upd, db, m, role, chatID)
	if err != nil {
		return
	}

	err = automaticChatIDChange(upd, db, groupID)
	if err != nil {
		return
	}

	if upd.Message.Chat.Type == privateCHat {
		switch {
		case upd.Message.Chat.Type == privateCHat && role == bomzRole:
			unknownUserMessageCase(bot, upd, chatID)
			return
		case m == "/start":
			sendKeyboardMessage(bot, chatID, m)
			return
		case m == "/stats":
			statsMessagesWithoutReset(bot, db, chatID, role)
			return
		case m == "хочу играть" || m == "не хочу играть":
			letsPlay(bot, db, chatID, groupID, userID, m)
			return
		case upd.Message.ReplyToMessage != nil && m == "user":
			giveUserRole(bot, upd, db, role, chatID)
			return

		default:
			return
		}
	}
	if upd.Message.ReplyToMessage != nil && m == "user" {
		giveUserRole(bot, upd, db, role, chatID)
		return
	}

	return
}

func sendMessage(bot *telego.Bot, ChatID int64, text string) error {
	msg := tu.Message(
		tu.ID(ChatID),
		text,
	)

	_, err := bot.SendMessage(msg)
	if err != nil {
		log.Printf("Не смог ответить %d пользователю на сообщение '%s': %s", ChatID, text, err)
		return err
	}
	return nil
}

func sendKeyboardMessage(bot *telego.Bot, ChatID int64, text string) {
	keyboard := tu.Keyboard(
		tu.KeyboardRow(
			tu.KeyboardButton("Хочу играть"),
			tu.KeyboardButton("Не хочу играть"),
		),
	).WithResizeKeyboard()

	msg := tu.Message(
		tu.ID(ChatID),
		"Привет",
	).WithReplyMarkup(keyboard)

	_, err := bot.SendMessage(msg)
	if err != nil {
		log.Printf("Не смог ответить %d пользователю на сообщение : %s", ChatID, err)
	}
}
func sendMessageIfCheckErrorNoNeed(bot *telego.Bot, ChatID int64, text string) {
	msg := tu.Message(
		tu.ID(ChatID),
		text,
	)

	_, err := bot.SendMessage(msg)
	if err != nil {
		log.Printf("Не смог ответить %d пользователю на сообщение '%s': %s", ChatID, text, err)
	}
}

func sendStatsAndDeleteBucketEveryNoon(bot *telego.Bot, db *database, stopChan chan struct{}) {
	const noonHour = 12

	noonCh := make(chan bool)
	groupID, err := db.ReadChatID()
	if err != nil {
		log.Printf("Не смог прочитать group id из бакета %s :%s", chatIDBucket, err)
	}
	go func() {
		for {
			t := time.NewTimer(timeToNextHour())

			select {
			case <-stopChan:
				t.Stop()
				return
			case <-t.C:
				t.Reset(timeToNextHour())

				if time.Now().Hour() == noonHour {
					noonCh <- true
				}
			}
		}
	}()

	for {
		select {
		case <-stopChan:
			return
		case <-noonCh:
		}

		err = db.ResetBucket(playersTodayBucket)
		if err != nil {
			log.Printf("cant delete %s bucket info: %s", playersTodayBucket, err)
			continue
		}

		statString, err := statsMessages(db, bot)
		if err != nil {
			log.Printf("reveiving stats: %s", err)
			continue
		}

		err = sendMessage(bot, groupID, statString)
		if err != nil {
			log.Printf("could not show users: %s", err)
			continue
		}
		err = db.ResetBucket(counterEndingBucket)
		if err != nil {
			log.Printf("could not delete bucket info: %s", err)
			continue
		}
	}
}

// timeToNextHour returns duration to the next hour.
func timeToNextHour() time.Duration {
	const minutesInHour = 60
	currMinutes := time.Now().Minute()
	return time.Minute * time.Duration(minutesInHour-currMinutes)
}

func statsMessages(db *database, bot *telego.Bot) (string, error) {
	groupID, err := db.ReadChatID()
	if err != nil {
		log.Printf("Не смог прочитать group id из бакета %s :%s", chatIDBucket, err)
	}
	dayStats, err := db.GetUsersEndingStat()
	if err != nil {
		log.Printf("could not show users: %s", err)
		return "", fmt.Errorf("could not get stat from db: %w", err)
	}
	infiniteStats, err := db.GetUsersInfiniteStat()
	if err != nil {
		log.Printf("could not show users: %s", err)
		return "", fmt.Errorf("could not get stat from db: %w", err)
	}

	var sb strings.Builder
	dayMessagesFound := len(dayStats)
	sb.WriteString("Messages for last update:\n")
	if dayMessagesFound > 0 {
		for _, stat := range dayStats {
			statName64, err := strconv.ParseInt(stat.Name, 10, 64)
			if err != nil {
				log.Printf("Не смог преобразовать string '%s' в int64", stat.Name)
				return "", err
			}
			username, err := getUsernameFromUserID(bot, statName64, groupID)
			if err == nil {
				sb.WriteString(fmt.Sprintf("%s: %d messages\n", username, stat.MessageCount))
			}
		}
	} else {
		sb.WriteString("\n0 сообщений, не забудь тыкнуть кнопку, что хочешь играть, если хочешь играть сегодня\n")
	}

	sb.WriteString("\nMessages for all time:\n")
	for _, stat := range infiniteStats {
		statName64, err := strconv.ParseInt(stat.Name, 10, 64)
		if err != nil {
			log.Printf("Не смог преобразовать string '%s' в int64", stat.Name)
			return "", err
		}
		username, err := getUsernameFromUserID(bot, statName64, groupID)
		if err == nil {
			sb.WriteString(fmt.Sprintf("%s: %d messages\n", username, stat.MessageCount))
		}
	}

	return sb.String(), nil
}

func getUsernameFromUserID(bot *telego.Bot, userID int64, chatID int64) (string, error) {
	prm := &telego.GetChatMemberParams{
		ChatID: telego.ChatID{
			ID: chatID,
		},
		UserID: userID,
	}

	chatMember, err := bot.GetChatMember(prm)
	if err != nil {
		log.Printf("Не смог узнать username пользователя %d : %s", userID, err)
		return "", err
	}

	user := chatMember.MemberUser()

	if user.Username != "" {
		return "@" + user.Username, nil
	}
	name := user.FirstName + " " + user.LastName
	if name == " " {
		return strconv.FormatInt(userID, 10), nil
	}
	return name, nil
}

func increaseMessagesCounter(bot *telego.Bot, db *database, chatID int64, groupID int64, userID int64) error {
	if chatID == groupID {
		err := db.IncUserMessages(strconv.FormatInt(userID, 10))
		if err != nil {
			log.Printf("couldnt increase counter: %s", err)
			sendMessageIfCheckErrorNoNeed(bot, chatID, "Что-то пошло не так, счетчик не работает")

			return err
		}
	}
	return nil
}

func nonAutomaticChatIDChange(bot *telego.Bot, upd telego.Update, db *database, m string, role byte, chatID int64) error {
	if strings.Contains(m, "chatid") && role == adminRole && upd.Message.Chat.Type == privateCHat {
		msgChatID := strings.Fields(m)
		chatIDGroup := msgChatID[len(msgChatID)-1]
		chatID64, err := strconv.ParseInt(chatIDGroup, 10, 64)
		if err != nil {
			log.Printf("Не смог преобразовать string %s в int64", chatIDGroup)
			sendMessageIfCheckErrorNoNeed(bot, chatID, "Проверь, скорее всего неправильный запрос")
			return err
		}
		err = db.SaveChatID(chatID64)
		if err != nil {
			log.Printf("Не смог записать новый chatID %d в бакет", chatID64)
			return err
		}
		err = sendMessage(bot, chatID, "Успешно изменили chatID на: "+strconv.FormatInt(chatID64, 10))
		if err != nil {
			return err
		}
	}
	return nil
}

func automaticChatIDChange(upd telego.Update, db *database, groupID int64) error {
	if upd.Message.MigrateFromChatID == groupID {
		err := db.SaveChatID(upd.Message.MigrateToChatID)
		if err != nil {
			log.Printf("не смог записать ID группы в бакет %s после миграции err: %s ", chatIDBucket, err)
		}
	}
	return nil
}

func unknownUserMessageCase(bot *telego.Bot, upd telego.Update, chatID int64) {
	err := sendMessage(bot, chatID, "Hi, if u want to use our cute bot - write @daunitse")
	if err != nil {
		return
	}
	sendMessageIfCheckErrorNoNeed(bot, daunitseID, "Он со мной связался\n"+upd.Message.From.FirstName+" @"+upd.Message.From.Username)
}

func statsMessagesWithoutReset(bot *telego.Bot, db *database, chatID int64, role byte) {
	if role < userRole {
		sendMessageIfCheckErrorNoNeed(bot, chatID, "Команда доступна только пользователям бота\nЕсли хочешь меня юзать - напиши @daunitse в лс")
		return
	}
	statString, err := statsMessages(db, bot)
	if err != nil {
		log.Printf("reveiving stats: %s", err)
		return
	}

	err = sendMessage(bot, chatID, statString)
	if err != nil {
		return
	}
}

func giveUserRole(bot *telego.Bot, upd telego.Update, db *database, role byte, chatID int64) {
	if role != adminRole {
		sendMessageIfCheckErrorNoNeed(bot, chatID, "Тебе нельзя")
		return
	}
	repliedRole, err := db.UserRole(upd.Message.ReplyToMessage.From.ID)
	if err != nil {
		log.Printf("не смог проверить роль пересылаемого пользователя, ошибка: %s", err)
		return
	}
	if repliedRole == adminRole {
		sendMessageIfCheckErrorNoNeed(bot, chatID, "Нельзя домогаться до админа")
		return
	}
	err = db.GiveUserRoles(upd.Message.ReplyToMessage.From.ID, userRole)
	if err != nil {
		log.Printf("Не смог назначить пользователю роль : %s", err)
		return
	}
	sendMessageIfCheckErrorNoNeed(bot, chatID, "Теперь он полноценный пользователь")
}

func letsPlay(bot *telego.Bot, db *database, chatID int64, groupID int64, userID int64, m string) {

	var status uint32

	if m == "хочу играть" {
		status = ready
	}
	err := db.SaveLetsPlayStatus(strconv.FormatInt(userID, 10), status)
	if err != nil {
		log.Printf("couldnt change status: %s", err)
		sendMessageIfCheckErrorNoNeed(bot, chatID, "Что-то пошло не так, не смог записать статус игрока")

		return
	}

	users, err := db.GetLetsPlayStatus()
	if err != nil {
		log.Printf("could not get users status: %s", err)
		sendMessageIfCheckErrorNoNeed(bot, chatID, "Не удалось получить список сопартийцев.")

		return
	}

	askNick, err := getUsernameFromUserID(bot, chatID, groupID)
	if err != nil {
		log.Printf("Не смог узнать юзернейм пользователя:%d chatID: %d", userID, chatID)
		sendMessageIfCheckErrorNoNeed(bot, chatID, "Ты как сюда попал?")

		return
	}

	var userNames []string

	if status == notReady {
		sendMessageIfCheckErrorNoNeed(bot, chatID, "Ну и пошел нахуй")

		for _, user := range users {
			if user.Status == ready {
				userInt64, err := strconv.ParseInt(user.Name, 10, 64)
				if err != nil {
					log.Printf("Не смог преобразовать данные в int64 из бакета playersTodayBucket %s", err)
					return
				}
				nick, err := getUsernameFromUserID(bot, userInt64, groupID)
				if err != nil {
					log.Printf("Не смог узнать юзернейм пользователя:%s chatID: %d", user.Name, userInt64)
				}
				if askNick == nick {
					continue
				}
				sendMessageIfCheckErrorNoNeed(bot, userInt64, "Этот пидор больше не хочет играть "+askNick)

			}
		}

		return
	}

	sendMessageIfCheckErrorNoNeed(bot, chatID, "Ищу сопартийцев... ")

	for _, user := range users {
		if user.Status == ready {
			userInt64, err := strconv.ParseInt(user.Name, 10, 64)
			if err != nil {
				log.Printf("Не смог преобразовать данные в int64 из бакета playersTodayBucket %s", err)
				return
			}
			nick, err := getUsernameFromUserID(bot, userInt64, groupID)
			if err != nil {
				log.Printf("Не смог узнать юзернейм пользователя:%s chatID: %d", user.Name, userInt64)
			}
			if askNick == nick {
				continue
			}
			sendMessageIfCheckErrorNoNeed(bot, userInt64, "Найден сопартиец "+askNick)

			userNames = append(userNames, nick)
		}
	}

	if len(userNames) > 0 {
		message := "Сопартийцы: " + strings.Join(userNames, " ")
		sendMessageIfCheckErrorNoNeed(bot, chatID, message)

		return
	}
	sendMessageIfCheckErrorNoNeed(bot, chatID, "К сожалению, нет доступных сопартийцев.")

	return
}

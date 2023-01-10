package internal

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"io"
	"net/http"
	"os/exec"
	"strings"

	"github.com/wdvxdr1123/go-silk"
)

var (
	httpClient = &http.Client{
		Transport: &http.Transport{
			ForceAttemptHTTP2:   true,
			MaxConnsPerHost:     0,
			MaxIdleConns:        0,
			MaxIdleConnsPerHost: 256,
		},
	}

	UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/87.0.4280.88 Safari/537.36 Edg/87.0.664.66"

	replacer = strings.NewReplacer(
		"[微笑]", "😃", "[Smile]", "😃",
		"[撇嘴]", "😖", "[Grimace]", "😖",
		"[色]", "😍", "[Drool]", "😍",
		"[发呆]", "😳", "[Scowl]", "😳",
		"[得意]", "😎", "[Chill]", "😎",
		"[流泪]", "😭", "[Sob]", "😭",
		"[害羞]", "☺️", "[Shy]", "☺️",
		"[闭嘴]", "🤐", "[Shutup]", "🤐",
		"[睡]", "😴", "[Sleep]", "😴",
		"[大哭]", "😣", "[Cry]", "😣",
		"[尴尬]", "😰", "[Awkward]", "😰",
		"[发怒]", "😡", "[Pout]", "😡",
		"[调皮]", "😜", "[Wink]", "😜",
		"[呲牙]", "😁", "[Grin]", "😁",
		"[惊讶]", "😱", "[Surprised]", "😱",
		"[难过]", "🙁", "[Frown]", "🙁",
		"[囧]", "☺️", "[Tension]", "☺️",
		"[抓狂]", "😫", "[Scream]", "😫",
		"[吐]", "🤢", "[Puke]", "🤢",
		"[偷笑]", "🙈", "[Chuckle]", "🙈",
		"[愉快]", "☺️", "[Joyful]", "☺️",
		"[白眼]", "🙄", "[Slight]", "🙄",
		"[傲慢]", "😕", "[Smug]", "😕",
		"[困]", "😪", "[Drowsy]", "😪",
		"[惊恐]", "😱", "[Panic]", "😱",
		"[流汗]", "😓", "[Sweat]", "😓",
		"[憨笑]", "😄", "[Laugh]", "😄",
		"[悠闲]", "😏", "[Loafer]", "😏",
		"[奋斗]", "💪", "[Strive]", "💪",
		"[咒骂]", "😤", "[Scold]", "😤",
		"[疑问]", "❓", "[Doubt]", "❓",
		"[嘘]", "🤐", "[Shhh]", "🤐",
		"[晕]", "😲", "[Dizzy]", "😲",
		"[衰]", "😳", "[BadLuck]", "😳",
		"[骷髅]", "💀", "[Skull]", "💀",
		"[敲打]", "👊", "[Hammer]", "👊",
		"[再见]", "🙋\u200d♂", "[Bye]", "🙋\u200d♂",
		"[擦汗]", "😥", "[Relief]", "😥",
		"[抠鼻]", "🤷\u200d♂", "[DigNose]", "🤷\u200d♂",
		"[鼓掌]", "👏", "[Clap]", "👏",
		"[坏笑]", "👻", "[Trick]", "👻",
		"[左哼哼]", "😾", "[Bah！L]", "😾",
		"[右哼哼]", "😾", "[Bah！R]", "😾",
		"[哈欠]", "😪", "[Yawn]", "😪",
		"[鄙视]", "😒", "[Lookdown]", "😒",
		"[委屈]", "😣", "[Wronged]", "😣",
		"[快哭了]", "😔", "[Puling]", "😔",
		"[阴险]", "😈", "[Sly]", "😈",
		"[亲亲]", "😘", "[Kiss]", "😘",
		"[可怜]", "😻", "[Whimper]", "😻",
		"[菜刀]", "🔪", "[Cleaver]", "🔪",
		"[西瓜]", "🍉", "[Melon]", "🍉",
		"[啤酒]", "🍺", "[Beer]", "🍺",
		"[咖啡]", "☕", "[Coffee]", "☕",
		"[猪头]", "🐷", "[Pig]", "🐷",
		"[玫瑰]", "🌹", "[Rose]", "🌹",
		"[凋谢]", "🥀", "[Wilt]", "🥀",
		"[嘴唇]", "💋", "[Lip]", "💋",
		"[爱心]", "❤️", "[Heart]", "❤️",
		"[心碎]", "💔", "[BrokenHeart]", "💔",
		"[蛋糕]", "🎂", "[Cake]", "🎂",
		"[炸弹]", "💣", "[Bomb]", "💣",
		"[便便]", "💩", "[Poop]", "💩",
		"[月亮]", "🌃", "[Moon]", "🌃",
		"[太阳]", "🌞", "[Sun]", "🌞",
		"[拥抱]", "🤗", "[Hug]", "🤗",
		"[强]", "👍", "[Strong]", "👍",
		"[弱]", "👎", "[Weak]", "👎",
		"[握手]", "🤝", "[Shake]", "🤝",
		"[胜利]", "✌️", "[Victory]", "✌️",
		"[抱拳]", "🙏", "[Salute]", "🙏",
		"[勾引]", "💁\u200d♂", "[Beckon]", "💁\u200d♂",
		"[拳头]", "👊", "[Fist]", "👊",
		"[OK]", "👌",
		"[跳跳]", "💃", "[Waddle]", "💃",
		"[发抖]", "🙇", "[Tremble]", "🙇",
		"[怄火]", "😡", "[Aaagh!]", "😡",
		"[转圈]", "🕺", "[Twirl]", "🕺",
		"[嘿哈]", "🤣", "[Hey]", "🤣",
		"[捂脸]", "🤦\u200d♂", "[Facepalm]", "🤦\u200d♂",
		"[奸笑]", "😜", "[Smirk]", "😜",
		"[机智]", "🤓", "[Smart]", "🤓",
		"[皱眉]", "😟", "[Concerned]", "😟",
		"[耶]", "✌️", "[Yeah!]", "✌️",
		"[红包]", "🧧", "[Packet]", "🧧",
		"[鸡]", "🐥", "[Chick]", "🐥",
		"[蜡烛]", "🕯️", "[Candle]", "🕯️",
		"[糗大了]", "😥",
		"[ThumbsUp]", "👍", "[ThumbsDown]", "👎",
		"[Peace]", "✌️",
		"[Pleased]", "😊",
		"[Rich]", "🀅",
		"[Pup]", "🐶",
		"[吃瓜]", "🙄\u200d🍉", "[Onlooker]", "🙄\u200d🍉",
		"[加油]", "💪\u200d😁", "[GoForIt]", "💪\u200d😁",
		"[加油加油]", "💪\u200d😷",
		"[汗]", "😓", "[Sweats]", "😓",
		"[天啊]", "😱", "[OMG]", "😱",
		"[Emm]", "🤔",
		"[社会社会]", "😏", "[Respect]", "😏",
		"[旺柴]", "🐶\u200d😏", "[Doge]", "🐶\u200d😏",
		"[好的]", "😏\u200d👌", "[NoProb]", "😏\u200d👌",
		"[哇]", "🤩", "[Wow]", "🤩",
		"[打脸]", "😟\u200d🤚", "[MyBad]", "😟\u200d🤚",
		"[破涕为笑]", "😂", "[破涕為笑]", "😂", "[Lol]", "😂",
		"[苦涩]", "😭", "[Hurt]", "😭",
		"[翻白眼]", "🙄", "[Boring]", "🙄",
		"[裂开]", "🫠", "[Broken]", "🫠",
		"[爆竹]", "🧨", "[Firecracker]", "🧨",
		"[烟花]", "🎆", "[Fireworks]", "🎆",
		"[福]", "🧧", "[Blessing]", "🧧",
		"[礼物]", "🎁", "[Gift]", "🎁",
		"[庆祝]", "🎉", "[Party]", "🎉",
		"[合十]", "🙏", "[Worship]", "🙏",
		"[叹气]", "😮‍💨", "[Sigh]", "😮‍💨",
		"[让我看看]", "👀", "[LetMeSee]", "👀",
		"[666]", "6️⃣6️⃣6️⃣",
		"[无语]", "😑", "[Duh]", "😑",
		"[失望]", "😞", "[Let Down]", "😞",
		"[恐惧]", "😨", "[Terror]", "😨",
		"[脸红]", "😳", "[Flushed]", "😳",
		"[生病]", "😷", "[Sick]", "😷",
		"[笑脸]", "😁", "[Happy]", "😁",
	)

	matrixReplacer = strings.NewReplacer(
		"🤷\u200d♂", "[抠鼻]", "🤷\u200d♂", "[DigNose]",
		"🤦\u200d♂", "[捂脸]", "🤦\u200d♂", "[Facepalm]",
		"🙄\u200d🍉", "[吃瓜]", "🙄\u200d🍉", "[Onlooker]",
		"💪\u200d😁", "[加油]", "💪\u200d😁", "[GoForIt]",
		"💪\u200d😷", "[加油加油]",
		"🐶\u200d😏", "[旺柴]", "🐶\u200d😏", "[Doge]",
		"😏\u200d👌", "[好的]", "😏\u200d👌", "[NoProb]",
		"😟\u200d🤚", "[打脸]", "😟\u200d🤚", "[MyBad]",
	)
)

const sampleRate = 24000
const numChannels = 1
const precision = 2

type waveHeader struct {
	RiffMark      [4]byte
	FileSize      int32
	WaveMark      [4]byte
	FmtMark       [4]byte
	FormatSize    int32
	FormatType    int16
	NumChans      int16
	SampleRate    int32
	ByteRate      int32
	BytesPerFrame int16
	BitsPerSample int16
	DataMark      [4]byte
	DataSize      int32
}

func convertToOgg(rawData []byte) ([]byte, error) {
	pcmData, err := silk.DecodeSilkBuffToPcm(rawData, sampleRate)
	if err != nil {
		return nil, err
	}

	header := waveHeader{
		RiffMark:      [4]byte{'R', 'I', 'F', 'F'},
		FileSize:      int32(44 + len(pcmData)),
		WaveMark:      [4]byte{'W', 'A', 'V', 'E'},
		FmtMark:       [4]byte{'f', 'm', 't', ' '},
		FormatSize:    16,
		FormatType:    1,
		NumChans:      int16(numChannels),
		SampleRate:    int32(sampleRate),
		ByteRate:      int32(sampleRate * numChannels * precision),
		BytesPerFrame: int16(numChannels * precision),
		BitsPerSample: int16(precision) * 8,
		DataMark:      [4]byte{'d', 'a', 't', 'a'},
		DataSize:      int32(len(pcmData)),
	}

	buf := &bytes.Buffer{}
	if err := binary.Write(buf, binary.LittleEndian, &header); err != nil {
		return nil, err
	}
	if _, err := buf.Write(pcmData); err != nil {
		return nil, err
	}

	cmd := exec.Command(
		"ffmpeg", "-i", "pipe:0", "-c:a", "libopus", "-b:a", "24K", "-f", "ogg", "pipe:1",
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	io.Copy(stdin, buf)
	stdin.Close()

	outputBuf := &bytes.Buffer{}
	io.Copy(outputBuf, stdout)

	if err := cmd.Wait(); err != nil {
		return nil, err
	}

	return outputBuf.Bytes(), nil
}

func GetBytes(url string) ([]byte, error) {
	reader, err := HTTPGetReadCloser(url)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = reader.Close()
	}()

	return io.ReadAll(reader)
}

type gzipCloser struct {
	f io.Closer
	r *gzip.Reader
}

func NewGzipReadCloser(reader io.ReadCloser) (io.ReadCloser, error) {
	gzipReader, err := gzip.NewReader(reader)
	if err != nil {
		return nil, err
	}

	return &gzipCloser{
		f: reader,
		r: gzipReader,
	}, nil
}

func (g *gzipCloser) Read(p []byte) (n int, err error) {
	return g.r.Read(p)
}

func (g *gzipCloser) Close() error {
	_ = g.f.Close()

	return g.r.Close()
}

func HTTPGetReadCloser(url string) (io.ReadCloser, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header["User-Agent"] = []string{UserAgent}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if strings.Contains(resp.Header.Get("Content-Encoding"), "gzip") {
		return NewGzipReadCloser(resp.Body)
	}

	return resp.Body, err
}

func ReplaceEmotion(content string) string {
	return replacer.Replace(content)
}

func ReplaceToEmotion(content string) string {
	return matrixReplacer.Replace(content)
}

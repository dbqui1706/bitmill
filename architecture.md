# 🏛️ Tài Liệu Thiết Kế Kiến Trúc Hệ Thống Bitmill

Tài liệu này được thiết kế theo nguyên lý **C4 Model (Phân tầng từ Tổng quan đến Chi tiết)**:
1. **Phần 1: Góc nhìn tổng quan toàn hệ thống (System Landscape)** — Tương tự bức tranh toàn cảnh hạ tầng.
2. **Phần 2: Bóc tách chi tiết từng Service phức tạp (Deep Dive Services)** — Giải thích cấu trúc bên trong của từng động cơ xử lý.

---

## PHẦN 1: GÓC NHÌN TỔNG QUAN HỆ THỐNG (SYSTEM OVERVIEW)

Sơ đồ dưới đây thể hiện toàn bộ luồng di chuyển từ **Clients $\rightarrow$ Gateways $\rightarrow$ Services $\rightarrow$ Messaging $\rightarrow$ Storage**:

```mermaid
flowchart LR
    %% 1. CLIENTS
    subgraph Col_Clients["1. CLIENTS"]
        Streamer["🎥 Streamer (OBS Studio)"]
        Viewer["📱 Khán giả (Web / Mobile)"]
        CDN["☁️ CDN Edge Caching"]
    end

    %% 2. GATEWAYS
    subgraph Col_Gateways["2. GATEWAYS"]
        RTMP_GW["⚡ RTMP Ingest Gateway\n(TCP Port :1935)"]
        API_GW["🌐 HTTP & WS Gateway\n(GIN Engine :8080)"]
    end

    %% 3. SERVICES
    subgraph Col_Services["3. SERVICES"]
        direction TB
        Live_Engine["🔴 Live Streaming Engine\n(Realtime Slicer)"]
        VOD_Engine["🎬 VOD Pipeline Engine\n(Batch Transcoder)"]
        Chat_Engine["💬 Real-time Chat Hub\n(WebSocket Manager)"]
        User_Channel["👤 User & Channel Service\n(Auth & Stream Keys)"]
    end

    %% 4. MESSAGING
    subgraph Col_Messaging["4. MESSAGING & EVENTS"]
        EventBus["📢 In-Process Event Bus\n(Stream Events & Job Queue)"]
    end

    %% 5. DATA & STORAGE
    subgraph Col_Data["5. DATA STORES & STORAGE"]
        direction TB
        HLS_Storage[("💾 HLS Media Storage\n(/hls_storage: .m3u8 & .ts)")]
        State_Cache[("⚡ State & Cache\n(Viewer Count & Sessions)")]
        Metadata_DB[("🗄️ Metadata Database\n(Videos, Users, Channels)")]
    end

    %% KẾT NỐI VÀ LUỒNG DỮ LIỆU
    %% Luồng Live
    Streamer -->|1. Đẩy luồng RTMP| RTMP_GW
    RTMP_GW -->|2. Xác thực Stream Key| User_Channel
    RTMP_GW -->|3. Đẩy video thô| Live_Engine
    Live_Engine -->|4. Ghi HLS cửa sổ trượt| HLS_Storage
    Live_Engine -.->|5. Bắn sự kiện: StreamStarted| EventBus
    EventBus -.->|Thông báo Stream Live| Chat_Engine

    %% Luồng Xem (Viewer & CDN)
    Viewer -->|6. Kéo video HLS| CDN
    CDN -->|7. Kéo file gốc từ Origin| HLS_Storage
    Viewer <-->|8. Kết nối Chat WebSocket| API_GW
    API_GW <-->|9. Nhận/Gửi tin nhắn| Chat_Engine
    Chat_Engine <-->|Cập nhật số người xem| State_Cache

    %% Luồng VOD
    Viewer -->|10. Tải lên video MP4| API_GW
    API_GW -->|11. Đăng ký & Gửi Job| VOD_Engine
    VOD_Engine -->|12. Transcode HLS tĩnh| HLS_Storage
    VOD_Engine -->|13. Lưu thông tin video| Metadata_DB
```

### Bảng phân vai các cột trong sơ đồ tổng quan:

| Khối (Column) | Vai trò | Công nghệ thực thi |
| :--- | :--- | :--- |
| **1. Clients** | Nơi bắt đầu luồng (Streamer phát) và nơi kết thúc (Viewer xem & chat). | OBS Studio, Trình duyệt Web (Hls.js), Mobile App. |
| **2. Gateways** | Cổng đón tiếp: Phân tách rõ rệt giữa luồng đẩy video (RTMP TCP 1935) và luồng giao tiếp Web (HTTP/WS 8080). | TCP Listener chuẩn, GIN Web Framework. |
| **3. Services** | 4 động cơ nghiệp vụ độc lập: Live Engine, VOD Engine, Chat Hub, và Quản lý Kênh/User. | Modular Monolith Packages trong Go (`internal/`). |
| **4. Messaging** | Cầu nối bất đồng bộ giúp các service không phụ thuộc chặt vào nhau. | Go Channels / Event Bus nội bộ. |
| **5. Data & Storage** | Nơi lưu trữ file HLS (`.m3u8`, `.ts`), trạng thái phòng chat và metadata. | Disk/S3 Storage, In-memory Mutex / Redis, Metadata Store. |

---

## PHẦN 2: BÓC TÁCH CHI TIẾT TỪNG SERVICE PHỨC TẠP (DEEP DIVE)

Thay vì nhồi nhét chi tiết vào sơ đồ lớn, dưới đây là thiết kế chi tiết bên trong của 3 Service cốt lõi:

```
┌───────────────────────────────────────────────────────────────┐
│ MỤC LỤC BÓC TÁCH DỊCH VỤ                                      │
│  ├─ Service 1: Live Streaming & Transcoding Engine (Phức tạp) │
│  ├─ Service 2: VOD Pipeline Engine (Xử lý hàng đợi ngầm)      │
│  └─ Service 3: Real-time Chat & Presence Hub (Concurrency)    │
└───────────────────────────────────────────────────────────────┘
```

---

### 🔴 SERVICE 1: Live Streaming & Transcoding Engine

Service này chịu trách nhiệm: **Nhận video theo thời gian thực từ OBS và biến thành luồng HLS cho người xem với độ trễ thấp nhất có thể.**

```mermaid
flowchart LR
    subgraph Input["1. Tiếp nhận kết nối"]
        OBS["OBS Studio"] -->|TCP Handshake| Ingest["RTMP Listener (:1935)"]
    end

    subgraph HandshakeValidation["2. Bắt tay & Kiểm tra Key"]
        Ingest -->|C0/C1 Handshake| Parser["Packet Parser\n(Đọc 1537 bytes)"]
        Parser -->|Check StreamKey| Auth{"Key hợp lệ?"}
        Auth -- Sai --> Disconnect["Ngắt kết nối ngay"]
        Auth -- Đúng --> S0S1S2["Gửi lại S0/S1/S2 + C2"]
    end

    subgraph TranscodeEngine["3. Cắt HLS thời gian thực (FFmpeg)"]
        S0S1S2 -->|Pipe Raw Bytes| FFmpeg["Docker FFmpeg\n(-tune zerolatency -g 60 -hls_time 2)"]
        FFmpeg --> SlidingWindow["Sliding Window Slicer\n(Chỉ giữ 3-5 segments mới nhất)"]
    end

    subgraph Output["4. Xuất bản"]
        SlidingWindow -->|Ghi file| LiveStore["hls_storage/live/:channel_id/\n├── stream.m3u8 (Không có ENDLIST)\n├── segment_105.ts\n├── segment_106.ts\n└── segment_107.ts"]
    end
```

#### Thiết kế chi tiết bên trong:
* **Cơ chế Pipe**: Dữ liệu từ kết nối TCP được pipe trực tiếp vào `stdin` của FFmpeg container $\rightarrow$ **Zero-disk write** trước khi encode.
* **Cấu hình FFmpeg độ trễ thấp**:
  * `-tune zerolatency -preset ultrafast`: Ép CPU mã hóa khung hình ngay khi vừa nhận được.
  * `-hls_flags delete_segments`: Cứ một file `.ts` mới sinh ra thì xóa một file `.ts` cũ nhất đi để không làm tràn ổ cứng.
  * Không ghi thẻ `#EXT-X-ENDLIST` để trình duyệt tiếp tục kéo các đoạn mới.

---

### 🎬 SERVICE 2: VOD Pipeline Engine

Service này xử lý video đã quay xong. Mục tiêu là: **Chất lượng hình ảnh đẹp nhất, dung lượng nén nhỏ nhất, hỗ trợ tua (seeking).**

```mermaid
flowchart TD
    UploadReq["Client Upload MP4"] --> UploadHandler["HTTP Upload Handler"]
    UploadHandler --> SaveRaw["Lưu video gốc: /uploads/raw_xxx.mp4"]
    SaveRaw --> CreateEntity["Tạo Video Entity (Status: PENDING)"]
    
    CreateEntity --> JobQueue["Đưa Job vào hàng đợi Worker Queue"]
    
    subgraph WorkerPool["Transcoding Worker Pool (Chạy ngầm)"]
        JobQueue --> Worker["Worker Goroutine rảnh"]
        Worker --> UpdateStatus1["Đổi Status: PROCESSING"]
        Worker --> RunFFmpeg["Chạy Docker FFmpeg\n(-preset medium -crf 23 -g 60 -hls_time 4)"]
        RunFFmpeg --> MakeStaticHLS["Tạo Static HLS:\n├── stream.m3u8 (Có #EXT-X-ENDLIST)\n└── segment_000.ts ... segment_999.ts"]
        MakeStaticHLS --> UpdateStatus2["Đổi Status: READY & Gán HLS URL"]
    end
    
    UpdateStatus2 --> Notify["Ghi nhận Metadata vào Database"]
```

#### Thiết kế chi tiết bên trong:
* **Xử lý bất đồng bộ (Non-blocking HTTP)**: Client upload xong nhận ngay phản hồi `202 Accepted`. Toàn bộ quá trình nén và cắt HLS diễn ra trong background Goroutine.
* **Cấu hình FFmpeg chất lượng cao**:
  * `-preset medium`: Cho phép nén chậm hơn nhưng dung lượng nhẹ hơn 50% so với live stream.
  * Thẻ `#EXT-X-ENDLIST` ở cuối file `.m3u8` cho phép trình phát video (Player) tính toán toàn bộ thời lượng video và cho phép người dùng tua tự do.

---

### 💬 SERVICE 3: Real-time Chat & Presence Hub

Service này giải quyết bài toán: **Hàng nghìn người trong cùng 1 phòng livestream nhắn tin và cập nhật số người xem cùng lúc.**

```mermaid
flowchart TD
    Client1["Client A (WebSocket)"] -->|ws connect| RoomHub["Phòng Chat (Room Hub Goroutine)"]
    Client2["Client B (WebSocket)"] -->|ws connect| RoomHub
    ClientN["Client N (WebSocket)"] -->|ws connect| RoomHub

    subgraph InsideHub["Cấu trúc bên trong Room Hub"]
        direction TB
        ClientsMap["Danh sách kết nối:\nmap[ClientID]*ClientConnection"]
        BroadcastChan["broadcast chan []byte\n(Hàng đợi tin nhắn)"]
        RegisterChan["register / unregister chan\n(Đăng ký / Hủy kết nối)"]
    end

    RoomHub --- InsideHub

    Client1 -->|Gửi tin nhắn: 'Chào mọi người'| BroadcastChan
    BroadcastChan --> Fanout["Vòng lặp Fan-out (Select Channel)"]
    Fanout -->|Gửi tới từng Client| Client1
    Fanout -->|Gửi tới từng Client| Client2
    Fanout -->|Gửi tới từng Client| ClientN

    RegisterChan --> Counter["Bộ đếm Realtime: len(ClientsMap)"]
    Counter -.->|Broadcast số viewer| Fanout
```

#### Thiết kế chi tiết bên trong:
* **Hub Pattern & Single-Threaded State Management**:
  * Mỗi phòng chat được quản lý bởi một Goroutine duy nhất chạy vòng lặp `select` trên các Go Channels (`broadcast`, `register`, `unregister`).
  * **Không cần dùng Mutex Lock**: Tránh hoàn toàn lỗi Race Condition và Deadlock vì mọi thao tác thêm/xóa client hay phát tin nhắn đều tuần tự hóa qua Channel.
* **Presence (Bộ đếm người xem trực tiếp)**:
  * Khi client ngắt kết nối WebSocket $\rightarrow$ rơi vào `unregister channel` $\rightarrow$ bộ đếm giảm $\rightarrow$ tự động phát số người xem mới tới toàn phòng.

---

## 3. Tổng Kết: Cách Tiếp Cận Kiến Trúc Này Giúp Gì Cho Bạn?

1. **Không bị ngợp**: Khi nhìn vào hệ thống, bạn nhìn vào **Phần 1** để thấy vị trí của mình (Streamer đẩy vào đâu, Viewer lấy từ đâu).
2. **Dễ hiện thực hóa từng phần**: Khi muốn code tính năng Live, bạn chỉ cần mở sơ đồ **Service 1**. Khi muốn làm Chat, chỉ cần nhìn **Service 3**.
3. **Mã nguồn tương ứng 100%**:
   - `internal/interfaces/http` và `internal/interfaces/rtmp` $\rightarrow$ Khối **Gateways**.
   - `internal/application/video` và `internal/domain/video` $\rightarrow$ Khối **Services**.
   - `internal/infrastructure/transcoder` $\rightarrow$ Khối **FFmpeg Engine**.

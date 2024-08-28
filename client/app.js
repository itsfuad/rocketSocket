let currentRoom = 'global';

const ws = new WebSocket('/ws')

const form = document.getElementById('form');
const message = document.getElementById('message');
const messages = document.getElementById('messageList');
const roomForm = document.getElementById('roomControls');
const usernameInput = document.getElementById('username');
const roomName = document.getElementById('roomName');
const actionButton = document.getElementById('actionButton');
const roomHeader = document.getElementById('roomHeader');

//on input change, update the action button disabled state
roomName.addEventListener('input', () => {
    actionButton.disabled = roomName.value.trim() === '' || usernameInput.value.trim() === '';
});

//on input change, update the action button disabled state
usernameInput.addEventListener('input', () => {
    actionButton.disabled = usernameInput.value.trim() === '' || roomName.value.trim() === '';
});

form.addEventListener('submit', (event) => {
    event.preventDefault();
    
    if (message.value.trim() === '') {
        return;
    }
    sendMessage(message.value, usernameInput.value);
});

actionButton.addEventListener('click', () => {
    if (actionButton.textContent === 'Join') {
        joinRoom(roomName.value);
    } else {
        leaveRoom(currentRoom);
    }
});

function handleJoin(){
    currentRoom = roomName.value;

    clearMessages();
    roomHeader.textContent = `Connected to Room: ${currentRoom}`;
    actionButton.style.backgroundColor = '#dc3545';
    actionButton.textContent = 'Leave';

    //disable room input and username input
    roomName.disabled = true;
    usernameInput.disabled = true;

    console.log(`Joined room: ${currentRoom}`);
}

function handleLeave(){
    console.log(`Left room: ${currentRoom}`);
    currentRoom = 'default';
    clearMessages();
    roomHeader.textContent = 'Join a room to chat';
    actionButton.style.backgroundColor = '#28a745';
    actionButton.textContent = 'Join';

    //enable room input and username input
    roomName.disabled = false;
    usernameInput.disabled = false;
}

ws.onopen = (e) => {
    console.log('Connected to the server', e);
    //joinRoom(currentRoom); // Automatically join the default room on connection
};

ws.onclose = () => {
    console.log('Disconnected from the server');
};

ws.onerror = (error) => {
    console.error(error);
};

// Parse JSON object to extract username and message content
ws.onmessage = (message) => {
    console.log('Received message:', message);
    const data = JSON.parse(message.data);
    const li = document.createElement('li');
    const username = document.createElement('div');
    username.classList.add('username');
    username.innerText = data.username;
    const msg = document.createElement('div');
    msg.classList.add('msg');
    msg.innerText = data.message;
    li.appendChild(username);
    li.appendChild(msg);
    // Apply different styling based on sender or receiver
    if (data.username === usernameInput.value) {
        li.classList.add('sent');
    } else if (data.username === 'Server') {
        li.classList.add('server');
        if (data.type === 'join'){
            li.classList.add('join');
        } else if (data.type === 'leave'){
            li.classList.add('leave');
        } else if (data.type === 'HI'){
            handleJoin();
            return;
        } else if (data.type === 'BYE'){
            handleLeave();
            return;
        }
    } else {
        li.classList.add('received');
    }
    messages.appendChild(li);
    messages.scrollTop = messages.scrollHeight;
};

// Modify sendMessage function to include username
function sendMessage(msg, username) {
    const data = JSON.stringify({
        action: 'message',
        room: currentRoom,
        username: username,
        message: msg
    });
    console.log('Sending message:', data);
    ws.send(data);
    // Clear the message input field after sending
    message.value = '';
}

function joinRoom(room) {
    const data = JSON.stringify({ action: 'join', room: room, username: usernameInput.value });
    ws.send(data);
}

function leaveRoom(room) {
    const data = JSON.stringify({ action: 'leave', room: room, username: usernameInput.value });
    ws.send(data);
}

function clearMessages() {
    while (messages.firstChild) {
        messages.removeChild(messages.firstChild);
    }
}
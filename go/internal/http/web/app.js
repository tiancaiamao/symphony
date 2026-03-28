// Symphony Kanban App

const API_BASE = '/api';
let pollInterval = null;
let currentTaskId = null;

// Initialize
document.addEventListener('DOMContentLoaded', () => {
    loadTasks();
    setupEventListeners();
    startPolling();
});

function setupEventListeners() {
    document.getElementById('refresh-btn').addEventListener('click', loadTasks);
    document.getElementById('new-task-btn').addEventListener('click', openNewTaskModal);
    document.getElementById('task-modal').addEventListener('click', (e) => {
        if (e.target.id === 'task-modal') closeModal();
    });
}

function startPolling() {
    pollInterval = setInterval(loadTasks, 5000);
}

function stopPolling() {
    if (pollInterval) {
        clearInterval(pollInterval);
        pollInterval = null;
    }
}

// API Functions
async function fetchTasks() {
    const response = await fetch(`${API_BASE}/tasks`);
    if (!response.ok) throw new Error('Failed to fetch tasks');
    const data = await response.json();
    return data.tasks || [];
}

async function createTask(title, description) {
    const response = await fetch(`${API_BASE}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title, description })
    });
    if (!response.ok) throw new Error('Failed to create task');
    return response.json();
}

async function updateTask(id, title, description, state) {
    const body = { title, description };
    if (state) body.state = state;
    const response = await fetch(`${API_BASE}/tasks/${id}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body)
    });
    if (!response.ok) throw new Error('Failed to update task');
    return response.json();
}

async function deleteTask(id) {
    const response = await fetch(`${API_BASE}/tasks/${id}`, {
        method: 'DELETE'
    });
    if (!response.ok) throw new Error('Failed to delete task');
    return response.json();
}

async function retryTask(id) {
    const response = await fetch(`${API_BASE}/tasks/${id}/retry`, {
        method: 'POST'
    });
    if (!response.ok) throw new Error('Failed to retry task');
    return response.json();
}

// Render Functions
async function loadTasks() {
    try {
        const tasks = await fetchTasks();
        renderBoard(tasks);
    } catch (error) {
        console.error('Error loading tasks:', error);
    }
}

function renderBoard(tasks) {
    const columns = {
        inbox: [],
        todo: [],
        running: [],
        'self-review': [],
        'address-comment': [],
        done: [],
        archive: [],
        failed: []
    };

    tasks.forEach(task => {
        const state = task.state || 'inbox';
        if (columns[state]) {
            columns[state].push(task);
        }
    });

    renderColumn('inbox', columns.inbox);
    renderColumn('todo', columns.todo);
    renderColumn('running', columns.running);
    renderColumn('self-review', columns['self-review']);
    renderColumn('address-comment', columns['address-comment']);
    renderColumn('done', columns.done);
    renderColumn('archive', columns.archive);
    renderColumn('failed', columns.failed);

    document.getElementById('inbox-count').textContent = columns.inbox.length;
    document.getElementById('todo-count').textContent = columns.todo.length;
    document.getElementById('running-count').textContent = columns.running.length;
    document.getElementById('self-review-count').textContent = columns['self-review'].length;
    document.getElementById('address-comment-count').textContent = columns['address-comment'].length;
    document.getElementById('done-count').textContent = columns.done.length;
    document.getElementById('archive-count').textContent = columns.archive.length;
    document.getElementById('failed-count').textContent = columns.failed.length;
}

function renderColumn(state, tasks) {
    const container = document.getElementById(`${state}-tasks`);
    const newContainer = container.cloneNode(false);
    container.parentNode.replaceChild(newContainer, container);
    
    if (tasks.length === 0) {
        newContainer.innerHTML = '<div class="empty-state">No tasks</div>';
    } else {
        newContainer.innerHTML = tasks.map(task => renderTaskCard(task)).join('');
        
        newContainer.querySelectorAll('.task-card').forEach(card => {
            card.addEventListener('dragstart', handleDragStart);
            card.addEventListener('dragend', handleDragEnd);
            card.addEventListener('click', (e) => {
                if (!card.classList.contains('dragging')) {
                    openTaskModal(card.dataset.id);
                }
            });
        });
    }
    
    newContainer.addEventListener('dragover', handleDragOver);
    newContainer.addEventListener('dragenter', handleDragEnter);
    newContainer.addEventListener('dragleave', handleDragLeave);
    newContainer.addEventListener('drop', handleDrop);
}

function renderTaskCard(task) {
    const timeAgo = formatTimeAgo(new Date(task.created_at));
    const retryBadge = task.retry_count > 0 
        ? `<span class="task-retry-count">retry ${task.retry_count}</span>` 
        : '';
    
    // PR badge
    let prBadge = '';
    if (task.pr_number > 0) {
        const prIcon = task.pr_state === 'merged' ? '✅' : task.pr_state === 'closed' ? '❌' : '🔀';
        const prText = `${prIcon} PR #${task.pr_number}`;
        prBadge = task.pr_url 
            ? `<a href="${escapeHtml(task.pr_url)}" target="_blank" class="task-pr-link" onclick="event.stopPropagation()">${prText}</a>`
            : `<span class="task-pr">${prText}</span>`;
    }
    
    return `
        <div class="task-card ${task.state}" data-id="${task.id}" draggable="true">
            <div class="task-title">${escapeHtml(task.title)}</div>
            <div class="task-meta">
                <span class="task-time">${timeAgo}</span>
                ${retryBadge}
            </div>
            ${prBadge ? `<div class="task-pr-info">${prBadge}</div>` : ''}
        </div>
    `;
}

// Modal Functions
function openNewTaskModal() {
    currentTaskId = null;
    document.getElementById('modal-title').textContent = 'New Task';
    document.getElementById('task-id').value = '';
    document.getElementById('task-title').value = '';
    document.getElementById('task-description').value = '';
    document.getElementById('task-state').value = 'inbox';
    document.getElementById('delete-btn').classList.add('hidden');
    document.getElementById('retry-btn').classList.add('hidden');
    document.getElementById('save-btn').classList.remove('hidden');
    document.getElementById('task-title').removeAttribute('readonly');
    document.getElementById('task-description').removeAttribute('readonly');
    showModal();
}

async function openTaskModal(id) {
    try {
        const response = await fetch(`${API_BASE}/tasks/${id}`);
        if (!response.ok) throw new Error('Failed to load task');
        const task = await response.json();
        
        currentTaskId = id;
        document.getElementById('modal-title').textContent = 'Task Details';
        document.getElementById('task-id').value = task.id;
        document.getElementById('task-title').value = task.title;
        document.getElementById('task-description').value = task.description || '';
        document.getElementById('task-state').value = task.state;
        
        document.getElementById('delete-btn').classList.remove('hidden');
        
        if (task.state === 'failed') {
            document.getElementById('retry-btn').classList.remove('hidden');
        } else {
            document.getElementById('retry-btn').classList.add('hidden');
        }
        
        if (task.state === 'running') {
            document.getElementById('task-title').setAttribute('readonly', true);
            document.getElementById('task-description').setAttribute('readonly', true);
            document.getElementById('save-btn').classList.add('hidden');
        } else {
            document.getElementById('task-title').removeAttribute('readonly');
            document.getElementById('task-description').removeAttribute('readonly');
            document.getElementById('save-btn').classList.remove('hidden');
        }
        
        showModal();
    } catch (error) {
        console.error('Error loading task:', error);
        alert('Failed to load task: ' + error.message);
    }
}

function showModal() {
    document.getElementById('task-modal').classList.remove('hidden');
}

function closeModal() {
    document.getElementById('task-modal').classList.add('hidden');
    currentTaskId = null;
}

async function saveTask() {
    const id = document.getElementById('task-id').value;
    const title = document.getElementById('task-title').value.trim();
    const description = document.getElementById('task-description').value.trim();
    
    if (!title) {
        alert('Title is required');
        return;
    }
    
    try {
        if (id) {
            await updateTask(id, title, description);
        } else {
            await createTask(title, description);
        }
        closeModal();
        loadTasks();
    } catch (error) {
        console.error('Error saving task:', error);
        alert('Failed to save task: ' + error.message);
    }
}

async function deleteCurrentTask() {
    const id = document.getElementById('task-id').value;
    if (!id) return;
    
    if (!confirm('Are you sure you want to delete this task?')) {
        return;
    }
    
    try {
        await deleteTask(id);
        closeModal();
        loadTasks();
    } catch (error) {
        console.error('Error deleting task:', error);
        alert('Failed to delete task: ' + error.message);
    }
}

async function retryCurrentTask() {
    const id = document.getElementById('task-id').value;
    if (!id) return;
    
    try {
        await retryTask(id);
        closeModal();
        loadTasks();
    } catch (error) {
        console.error('Error retrying task:', error);
        alert('Failed to retry task: ' + error.message);
    }
}

// Utility Functions
function formatTimeAgo(date) {
    const seconds = Math.floor((new Date() - date) / 1000);
    
    if (seconds < 60) return 'just now';
    const minutes = Math.floor(seconds / 60);
    if (minutes < 60) return `${minutes}m ago`;
    const hours = Math.floor(minutes / 60);
    if (hours < 24) return `${hours}h ago`;
    const days = Math.floor(hours / 24);
    if (days < 7) return `${days}d ago`;
    return date.toLocaleDateString();
}

function escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
}

// Drag and Drop Functions
let draggedTaskId = null;

function handleDragStart(e) {
    console.log('[DRAG] Drag start on', e.target.dataset.id);
    draggedTaskId = e.target.dataset.id;
    e.target.classList.add('dragging');
    e.dataTransfer.effectAllowed = 'move';
    e.dataTransfer.setData('text/plain', draggedTaskId);
}

function handleDragEnd(e) {
    console.log('[DRAG] Drag end');
    e.target.classList.remove('dragging');
    document.querySelectorAll('.column-body').forEach(list => {
        list.classList.remove('drag-over');
    });
}

function handleDragOver(e) {
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
}

function handleDragEnter(e) {
    e.preventDefault();
    e.currentTarget.classList.add('drag-over');
    console.log('[DRAG] Drag enter', e.currentTarget.id);
}

function handleDragLeave(e) {
    e.currentTarget.classList.remove('drag-over');
    console.log('[DRAG] Drag leave', e.currentTarget.id);
}

async function handleDrop(e) {
    e.preventDefault();
    const targetList = e.currentTarget;
    targetList.classList.remove('drag-over');
    
    if (!draggedTaskId) return;
    
    const newState = targetList.id.replace('-tasks', '');
    console.log('[DRAG] Moving task', draggedTaskId, 'to', newState);
    
    try {
        const response = await fetch(`${API_BASE}/tasks/${draggedTaskId}`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ state: newState })
        });
        
        if (!response.ok) {
            const error = await response.text();
            console.error('[DRAG] API error:', error);
            throw new Error('Failed to update task state: ' + error);
        }
        
        console.log('[DRAG] Task moved successfully');
        await loadTasks();
    } catch (error) {
        console.error('[DRAG] Error:', error);
        alert('Failed to move task: ' + error.message);
    }
    
    draggedTaskId = null;
}
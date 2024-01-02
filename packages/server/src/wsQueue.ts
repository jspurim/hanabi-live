// We handle all WebSocket logins and logouts using a queue to prevent race conditions.

import type { UserID } from "@hanabi/data";
import { ServerCommand, Settings } from "@hanabi/data";
import type { queueAsPromised } from "fastq";
import fastq from "fastq";
import { SECOND_IN_MILLISECONDS } from "isaacscript-common-ts";
import { logger } from "./logger";
import { models } from "./models";
import { getRedisTablesWithUser } from "./redis";
import { enqueueSetPlayerConnected } from "./tableQueue";
import { getSpectatingMetadata, getTableIDsUserPlayingAt } from "./tables";
import { wsError, wsSend, wsSendAll } from "./wsHelpers";
import { wsMessage } from "./wsMessage";
import type { WSUser } from "./wsUsers";
import { wsUsers } from "./wsUsers";

export enum WSQueueElementType {
  Login,
  Logout,
}

interface WSQueueElement extends WSUser {
  type: WSQueueElementType;
}

const QUEUE_FUNCTIONS = {
  [WSQueueElementType.Login]: login,
  [WSQueueElementType.Logout]: logout,
} as const satisfies Record<
  WSQueueElementType,
  (wsUser: WSUser) => void | Promise<void>
>;

const wsQueue: queueAsPromised<WSQueueElement, void> = fastq.promise(
  processQueue,
  1,
);

async function processQueue(element: WSQueueElement) {
  const func = QUEUE_FUNCTIONS[element.type];
  await func(element);
}

function login(wsUser: WSUser) {
  const { userID, username, ip, status, tableID, hyphenated, inactive } =
    wsUser;

  logger.info(
    `Logging in WebSocket user ${username} (${userID}) from IP: ${ip}`,
  );

  // First, check to see if an existing WebSocket connection exists for this user.
  const existingUser = wsUsers.get(userID);
  if (existingUser !== undefined) {
    wsError(
      existingUser.connection,
      "You have logged on from somewhere else, so you have been disconnected here.",
    );
    existingUser.connection.destroy();
  }

  // We perform a type assertion to a readable map to represent that we are inside of the WebSocket
  // login queue, which is the only place that this map should be mutable.
  (wsUsers as Map<UserID, WSUser>).set(userID, wsUser);

  attachWebSocketEventHandlers(wsUser);

  wsSendAll(ServerCommand.user, {
    userID,
    name: username,
    status,
    tableID,
    hyphenated,
    inactive,
  });

  // We intentionally do not await the sending of the initial messages because we want to do
  // database-intensive work out of the critical path.
  // eslint-disable-next-line @typescript-eslint/no-floating-promises
  sendInitialWSMessages(wsUser);
}

function attachWebSocketEventHandlers(wsUser: WSUser) {
  const { connection } = wsUser;

  connection.socket.on("message", (rawData) => {
    // WebSocket callbacks are supposed to be synchronous functions, so we do not bother awaiting
    // the results of the command.
    // eslint-disable-next-line @typescript-eslint/no-floating-promises
    wsMessage(wsUser, rawData);
  });

  connection.socket.on("close", () => {
    enqueueWSMsg(WSQueueElementType.Logout, wsUser);
  });
}

async function sendInitialWSMessages(wsUser: WSUser) {
  await sendWelcomeMessage(wsUser);

  /*
  sendUserList()
  sendTableList()
  sendChat()
  sendHistory()
  sendFriendHistory();
  */
  // TODO
}

async function sendWelcomeMessage(wsUser: WSUser) {
  const { connection, userID, username, muted } = wsUser;

  const totalGames = await models.games.getUserNumGames(userID, true);

  const datetimeCreated =
    (await models.users.getDatetimeCreated(userID)) ?? new Date();
  const elapsedTime = Date.now() - datetimeCreated.getTime();
  const firstTimeUser = elapsedTime > 10 * SECOND_IN_MILLISECONDS;

  const settings = (await models.userSettings.get(userID)) ?? new Settings();
  const friends = await models.userFriends.getList(userID);

  const playingAtTables = getTableIDsUserPlayingAt(userID);
  const spectatingMetadata = getSpectatingMetadata(userID);
  const disconSpectatingTable = spectatingMetadata?.tableID;
  const disconShadowingSeat = spectatingMetadata?.shadowingPlayerIndex;

  const randomTableName = ""; // TODO;
  const shuttingDown = false; // TODO;
  const datetimeShutdownInit = ""; // TODO
  const maintenanceMode = false; // TODO

  wsSend(connection, ServerCommand.welcome, {
    userID,
    username,
    totalGames,
    muted,
    firstTimeUser,
    settings,
    friends,

    playingAtTables,
    disconSpectatingTable,
    disconShadowingSeat,

    randomTableName,
    shuttingDown,
    datetimeShutdownInit,
    maintenanceMode,
  });
}

async function logout(wsUser: WSUser) {
  const { userID, username, ip } = wsUser;

  // Check to see if there is a newer WebSocket connection for this user that is already connected.
  // If so, we can skip the logout work. (This check is necessary because when the same user logs in
  // twice, a logout will be triggered for the first connection after the second one has already
  // connected.)
  const existingWSUser = wsUsers.get(userID);
  if (
    existingWSUser !== undefined &&
    existingWSUser.sessionID > wsUser.sessionID
  ) {
    return;
  }

  logger.info(
    `Logging out WebSocket user ${username} (${userID}) from IP: ${ip}`,
  );

  // We perform a type assertion to a readable map to represent that we are inside of the WebSocket
  // login queue, which is the only place that this map should be mutable.
  (wsUsers as Map<UserID, WSUser>).delete(userID);

  const tables = await getRedisTablesWithUser(userID);
  for (const table of tables) {
    enqueueSetPlayerConnected(table.id, userID, false);
  }

  wsSendAll(ServerCommand.userLeft, {
    userID,
  });
}

// ------------------
// Exported functions
// ------------------

export function enqueueWSMsg(type: WSQueueElementType, wsUser: WSUser): void {
  // eslint-disable-next-line @typescript-eslint/no-floating-promises
  wsQueue.push({
    ...wsUser,
    type,
  });
}